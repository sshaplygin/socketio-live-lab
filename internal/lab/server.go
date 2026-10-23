package lab

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	socketio "github.com/sshaplygin/go-socket.io"
	"github.com/sshaplygin/go-socket.io/engineio"
	"github.com/sshaplygin/go-socket.io/engineio/transport/polling"
	"github.com/sshaplygin/go-socket.io/engineio/transport/websocket"
)

type Server struct {
	Hub            *Hub
	IO             *socketio.Server
	mu             sync.Mutex
	peers          map[socketio.Conn]*Peer
	admissions     map[string]*window
	proxies        []*net.IPNet
	origins, hosts map[string]bool
	metricsToken   string
}

func NewServer(c Config, origins, hosts []string, metricsToken string) *Server {
	s := &Server{Hub: NewHub(c), peers: map[socketio.Conn]*Peer{}, admissions: map[string]*window{}, origins: map[string]bool{}, hosts: map[string]bool{}, metricsToken: metricsToken}
	for _, o := range origins {
		s.origins[strings.TrimSpace(o)] = true
	}
	for _, host := range hosts {
		s.hosts[strings.TrimSpace(host)] = true
	}
	checkOrigin := func(r *http.Request) bool { return r.Header.Get("Origin") == "" || s.origins[r.Header.Get("Origin")] }
	s.IO = socketio.NewServer(socketio.WithWriteBufferSize(c.QueueSize), socketio.WithCloseTimeout(2*time.Second), socketio.WithLogger(slog.Default()), socketio.WithEngineOptions(engineio.WithPingInterval(5*time.Second), engineio.WithPingTimeout(5*time.Second), engineio.WithMaxHTTPBufferSize(8192), engineio.WithCORS(false), engineio.WithTransports(&polling.Transport{CheckOrigin: checkOrigin}, &websocket.Transport{CheckOrigin: checkOrigin}), engineio.WithCookieEnabled(false)))
	s.IO.OnConnect(Namespace, func(conn socketio.Conn) error {
		// Serialize attachment with disconnect, including a loss during OnConnect.
		s.mu.Lock()
		defer s.mu.Unlock()
		p, err := s.Hub.Connect(conn)
		if err != nil {
			return err
		}
		token := strings.TrimPrefix(conn.RemoteHeader().Get("Authorization"), "Bearer ")
		if token != "" && !s.Hub.AuthenticateBot(p, token) {
			s.Hub.Disconnect(p)
			return fmt.Errorf("invalid bot credential")
		}
		if !conn.Connected() {
			s.Hub.Disconnect(p)
			return fmt.Errorf("connection closed during admission")
		}
		s.peers[conn] = p
		slog.Info("namespace connected", "type", p.kind)
		return nil
	})
	s.IO.OnDisconnect(Namespace, func(conn socketio.Conn, reason string) {
		s.mu.Lock()
		p := s.peers[conn]
		delete(s.peers, conn)
		s.mu.Unlock()
		if p != nil {
			s.Hub.Disconnect(p)
		}
		slog.Info("namespace disconnected", "reason", reason)
	})
	s.IO.OnError(Namespace, func(_ socketio.Conn, err error) {
		s.Hub.mu.Lock()
		s.Hub.metrics["unexpected_errors"]++
		s.Hub.mu.Unlock()
		slog.Warn("socket error", "errorType", fmt.Sprintf("%T", err))
	})
	for _, name := range []string{"lab:bootstrap", "lab:resync", "lab:send", "lab:probe", "lab:room:join", "lab:identity:shuffle", "lab:bot:ready", "lab:bot:echo", "lab:bot:reply", "lab:bot:chat"} {
		event := name
		// Decode an untyped argument ourselves so invalid field types receive our ACK envelope.
		s.IO.OnEvent(Namespace, event, func(conn socketio.Conn, value interface{}) Ack {
			s.mu.Lock()
			p := s.peers[conn]
			s.mu.Unlock()
			if p == nil {
				return failure("DISCONNECTED", "Connection is unavailable")
			}
			raw, err := json.Marshal(value)
			if err != nil || len(raw) > 4096 {
				return s.Hub.RejectPayload(p)
			}
			var r Request
			if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &r) != nil {
				return s.Hub.RejectPayload(p)
			}
			return s.Hub.Handle(p, event, r)
		})
	}
	return s
}

// TrustProxies sets the networks whose X-Forwarded-For the admission limit
// believes. Only list addresses that can reach the server solely as a reverse
// proxy which overwrites the header with the real client address.
func (s *Server) TrustProxies(cidrs []string) error {
	for _, cidr := range cidrs {
		if cidr = strings.TrimSpace(cidr); cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return err
		}
		s.proxies = append(s.proxies, network)
	}
	return nil
}

// clientIP keys the admission limit. A trusted proxy's own address would put
// every visitor into one shared budget, so its last X-Forwarded-For entry (the
// one the proxy wrote) is used instead; everything else gets the peer address.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	trusted := false
	for _, network := range s.proxies {
		if peer != nil && network.Contains(peer) {
			trusted = true
			break
		}
	}
	values := r.Header.Values("X-Forwarded-For")
	if !trusted || len(values) == 0 {
		return host
	}
	last := values[len(values)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
		return ip.String()
	}
	return host
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !s.hosts[r.Host] {
		http.Error(w, "Host not allowed", http.StatusForbidden)
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !s.origins[origin] {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.URL.Path {
	case "/socket.io/":
		if r.Method != "GET" && r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if r.URL.Query().Get("sid") == "" {
			ip := s.clientIP(r)
			s.mu.Lock()
			now := time.Now()
			for key, v := range s.admissions {
				if now.Sub(v.start) > time.Minute {
					delete(s.admissions, key)
				}
			}
			if s.admissions[ip] == nil && len(s.admissions) < 4096 {
				s.admissions[ip] = &window{}
			}
			limiter := s.admissions[ip]
			allowed := limiter != nil && limiter.allow(now, 60, time.Minute)
			s.mu.Unlock()
			if !allowed || s.IO.Count() >= s.Hub.config.MaxClients {
				http.Error(w, "Connection limit reached", http.StatusTooManyRequests)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		s.IO.ServeHTTP(w, r)
	case "/api/live-lab/status", "/readyz":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		status := s.Hub.Status()
		w.Header().Set("Content-Type", "application/json")
		if status.Status == "unavailable" {
			w.WriteHeader(503)
		}
		_ = json.NewEncoder(w).Encode(status)
	case "/healthz":
		w.WriteHeader(http.StatusOK)
	case "/metrics":
		if s.metricsToken == "" || r.Header.Get("Authorization") != "Bearer "+s.metricsToken {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Hub.Metrics())
	default:
		http.NotFound(w, r)
	}
}
