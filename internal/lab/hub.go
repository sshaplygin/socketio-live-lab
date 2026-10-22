package lab

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Config struct {
	MaxClients, HistoryLimit, HistoryBytes, MaxIdentities, QueueSize int
	HistoryAge                                                       time.Duration
	SubmissionsEnabled                                               bool
	BotToken                                                         string
}

func DefaultConfig() Config {
	return Config{MaxClients: 200, HistoryLimit: 100, HistoryBytes: 1 << 20, MaxIdentities: 2000, QueueSize: 64, HistoryAge: 15 * time.Minute, SubmissionsEnabled: true}
}

type Wire interface {
	Emit(string, ...interface{})
	Join(...string)
	Leave(string)
	Close() error
}
type delivery struct {
	name string
	data any
}
type window struct {
	start time.Time
	count int
}

func (w *window) allow(now time.Time, limit int, interval time.Duration) bool {
	if now.Sub(w.start) >= interval {
		w.start = now
		w.count = 0
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

type Peer struct {
	wire                       Wire
	out                        chan delivery
	stop                       chan struct{}
	closed                     bool
	identity                   *identityRecord
	kind, room                 string
	ready                      bool
	connectedAt                time.Time
	messages, controls, echoes window
	chatReplies                window
}
type accepted struct {
	receipt           Receipt
	at                time.Time
	text, scope, room string
}
type identityRecord struct {
	Identity
	lastSeen time.Time
	active   int
	dedup    map[string]accepted
}
type pendingEcho struct {
	origin, bot *Peer
	text, room  string
	deadline    time.Time
}
type Hub struct {
	mu                   sync.Mutex
	config               Config
	boot                 string
	peers                map[*Peer]bool
	identities           map[string]*identityRecord
	names                map[string]string
	sequences            map[string]uint64
	history              map[string][]Event
	historyBytes         int
	revision             uint64
	lastHuman, lastEvent *time.Time
	pending              map[string]pendingEcho
	metrics              map[string]uint64
	ready                bool
}

func NewHub(c Config) *Hub {
	return &Hub{config: c, boot: randomID(), peers: map[*Peer]bool{}, identities: map[string]*identityRecord{}, names: map[string]string{}, sequences: map[string]uint64{}, history: map[string][]Event{}, pending: map[string]pendingEcho{}, metrics: map[string]uint64{"connections_total": 0, "disconnections_total": 0, "reconnects": 0, "accepted_events": 0, "rejected_events": 0, "rate_limited": 0, "ack_timeouts": 0, "broadcast_failures": 0, "unexpected_errors": 0, "rejected_connections": 0}, ready: true}
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func validID(s string) bool {
	if len(s) != 32 && len(s) != 36 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func validRoom(s string) bool {
	for _, r := range Rooms {
		if r == s {
			return true
		}
	}
	return false
}
func (h *Hub) Connect(w Wire) (*Peer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready || len(h.peers) >= h.config.MaxClients {
		h.metrics["rejected_connections"]++
		return nil, fmt.Errorf("capacity unavailable")
	}
	p := &Peer{wire: w, out: make(chan delivery, h.config.QueueSize), stop: make(chan struct{}), kind: "browser", connectedAt: time.Now()}
	h.peers[p] = true
	h.metrics["connections_total"]++
	h.revision++
	h.publishPresence()
	go func() {
		for {
			select {
			case <-p.stop:
				return
			case d := <-p.out:
				w.Emit(d.name, d.data)
			}
		}
	}()
	return p, nil
}
func (h *Hub) Disconnect(p *Peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.peers[p] {
		return
	}
	delete(h.peers, p)
	if !p.closed {
		p.closed = true
		close(p.stop)
	}
	if p.identity != nil {
		p.identity.active--
		p.identity.lastSeen = time.Now()
	}
	for id, req := range h.pending {
		if req.origin == p {
			delete(h.pending, id)
		} else if req.bot == p {
			h.enqueue(req.origin, "lab:bot:result", Echo{CorrelationID: id, Error: "Go client disconnected"})
			delete(h.pending, id)
		}
	}
	h.metrics["disconnections_total"]++
	h.revision++
	if p.ready {
		h.record("system", "", Sender{ID: "server", Name: "server", Type: "server"}, h.peerName(p)+" disconnected")
	}
	h.publishPresence()
}
func (h *Hub) peerName(p *Peer) string {
	if p.kind == "go-client" {
		return "go_echo_bot"
	}
	if p.identity != nil {
		return p.identity.DisplayName
	}
	return "A connection"
}
func (h *Hub) enqueue(p *Peer, name string, data any) {
	if !h.peers[p] || p.closed {
		return
	}
	select {
	case p.out <- delivery{name, data}:
	default:
		h.metrics["broadcast_failures"]++
		p.closed = true
		close(p.stop)
		go p.wire.Close()
	}
}
func (h *Hub) identity(hint, style string) *identityRecord {
	now := time.Now()
	if !validID(hint) {
		hint = randomID()
	}
	if i := h.identities[hint]; i != nil {
		i.lastSeen = now
		return i
	}
	// Evict only inactive identities. Stable hints deterministically restore the base name/color.
	for len(h.identities) >= h.config.MaxIdentities {
		var oldest *identityRecord
		for _, i := range h.identities {
			if i.active == 0 && (oldest == nil || i.lastSeen.Before(oldest.lastSeen)) {
				oldest = i
			}
		}
		if oldest == nil {
			return nil
		}
		delete(h.names, oldest.DisplayName)
		delete(h.identities, oldest.VisitorID)
	}
	hash := sha256.Sum256([]byte(hint))
	if style == "" {
		style = "systems"
	}
	colors := []string{"#b5451b", "#3873bb", "#8c54b5", "#288577", "#b06425"}
	i := &identityRecord{Identity: Identity{VisitorID: hint, AvatarColor: colors[int(hash[2])%len(colors)], PublicID: hex.EncodeToString(hash[8:16])}, lastSeen: now, dedup: map[string]accepted{}}
	i.NameStyle = style
	i.DisplayName = h.allocateName(hint, style, hash[:])
	h.identities[hint] = i
	return i
}
func (h *Hub) allocateName(id, style string, seed []byte) string {
	words := nameStyles[style]
	base := words.prefixes[int(seed[0])%len(words.prefixes)] + "_" + words.nouns[int(seed[1])%len(words.nouns)]
	name := base
	for n := 2; h.names[name] != ""; n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	h.names[name] = id
	return name
}
func (h *Hub) reject(code, message string) Ack {
	h.metrics["rejected_events"]++
	if code == "RATE_LIMITED" {
		h.metrics["rate_limited"]++
	}
	return failure(code, message)
}
func (h *Hub) Handle(p *Peer, event string, r Request) Ack {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.peers[p] || p.closed {
		return failure("DISCONNECTED", "Connection is no longer active")
	}
	now := time.Now()
	if !p.controls.allow(now, 80, 10*time.Second) {
		return h.reject("RATE_LIMITED", "Too many requests")
	}
	if event == "lab:bootstrap" {
		if !validNameStyle(r.NameStyle) {
			return h.reject("INVALID_NAME_STYLE", "Choose a predefined name style")
		}
		if p.kind != "browser" {
			return h.reject("FORBIDDEN", "Browser bootstrap required")
		}
		if !p.ready {
			if r.BootID != "" {
				h.metrics["reconnects"]++
			}
			p.identity = h.identity(r.VisitorID, r.NameStyle)
			if p.identity == nil {
				return h.reject("CAPACITY", "Identity capacity reached")
			}
			p.identity.active++
			p.ready = true
			h.revision++
			h.record("system", "", Sender{ID: "server", Name: "server", Type: "server"}, p.identity.DisplayName+" connected")
			h.publishPresence()
		}
		return success(h.snapshot(p, r))
	}
	if event == "lab:bot:ready" {
		if p.kind != "go-client" {
			return h.reject("FORBIDDEN", "Go client authentication required")
		}
		if !p.ready {
			p.ready = true
			p.room = "developers"
			p.wire.Join(p.room)
			h.revision++
			h.record("system", "", Sender{ID: "server", Name: "server", Type: "server"}, "go_echo_bot is ready")
			h.publishPresence()
		}
		return success(map[string]bool{"ready": true})
	}
	if !p.ready {
		return h.reject("NOT_READY", "Complete bootstrap first")
	}
	if event == "lab:probe" {
		return success(map[string]any{"serverTime": now, "presence": h.presence()})
	}
	if event == "lab:bot:reply" {
		return h.botReply(p, r)
	}
	if event == "lab:bot:chat" {
		return h.botChat(p, r, now)
	}
	if p.kind != "browser" {
		return h.reject("FORBIDDEN", "Browser action required")
	}
	switch event {
	case "lab:resync":
		return success(h.snapshot(p, r))
	case "lab:room:join":
		if !validRoom(r.RoomID) {
			return h.reject("INVALID_ROOM", "Choose a predefined room")
		}
		if p.room != r.RoomID {
			if p.room != "" {
				p.wire.Leave(p.room)
			}
			p.room = r.RoomID
			p.wire.Join(p.room)
			h.revision++
			h.record("system", p.room, Sender{ID: "server", Name: "server", Type: "server"}, p.identity.DisplayName+" joined "+p.room)
			h.publishPresence()
		}
		return success(h.snapshot(p, r))
	case "lab:identity:shuffle":
		if !validNameStyle(r.NameStyle) {
			return h.reject("INVALID_NAME_STYLE", "Choose a predefined name style")
		}
		if !p.messages.allow(now, 5, 10*time.Second) {
			return h.reject("RATE_LIMITED", "Please wait before shuffling again")
		}
		old := p.identity.DisplayName
		seed := sha256.Sum256([]byte(randomID()))
		if r.NameStyle != "" {
			p.identity.NameStyle = r.NameStyle
		}
		p.identity.DisplayName = h.allocateName(p.identity.VisitorID, p.identity.NameStyle, seed[:])
		delete(h.names, old)
		h.revision++
		for q := range h.peers {
			if q.identity == p.identity {
				h.enqueue(q, "lab:identity", p.identity.Identity)
			}
		}
		h.publishPresence()
		return success(p.identity.Identity)
	case "lab:send":
		return h.send(p, r, now)
	case "lab:bot:echo":
		return h.echo(p, r, now)
	default:
		return h.reject("UNKNOWN_EVENT", "Unsupported application event")
	}
}
func (h *Hub) send(p *Peer, r Request, now time.Time) Ack {
	if !h.config.SubmissionsEnabled {
		return h.reject("DISABLED", "Interactive submissions are temporarily disabled")
	}
	text := strings.TrimSpace(r.Text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 500 || !validID(r.ClientMessageID) {
		return h.reject("INVALID_PAYLOAD", "Use 1–500 characters and a valid message ID")
	}
	if r.Scope != "global" && r.Scope != "room" {
		return h.reject("INVALID_SCOPE", "Choose Global or Room")
	}
	room := ""
	if r.Scope == "room" {
		room = r.RoomID
		if room == "" || room != p.room {
			return h.reject("NOT_IN_ROOM", "Join the selected room first")
		}
	}
	for id, a := range p.identity.dedup {
		if now.Sub(a.at) > h.config.HistoryAge {
			delete(p.identity.dedup, id)
		}
	}
	if a, ok := p.identity.dedup[r.ClientMessageID]; ok {
		if a.text != text || a.scope != r.Scope || a.room != room {
			return h.reject("ID_CONFLICT", "Message ID already used for different content")
		}
		return success(a.receipt)
	}
	if IsBotCommand(text) {
		var bot *Peer
		for q := range h.peers {
			if q.kind == "go-client" && q.ready && !q.closed {
				bot = q
				break
			}
		}
		if bot == nil {
			return h.reject("BOT_UNAVAILABLE", "The Go bot is offline. Try again when it reconnects.")
		}
		if room != "" && room != bot.room {
			return h.reject("BOT_WRONG_ROOM", "Bot commands work in Everyone and Developers. Switch chats to use them.")
		}
	}
	if !p.messages.allow(now, 5, 10*time.Second) {
		return h.reject("RATE_LIMITED", "Send up to five messages per ten seconds")
	}
	e := h.record("message", room, Sender{ID: p.identity.PublicID, Name: p.identity.DisplayName, Type: "browser", Color: p.identity.AvatarColor}, text)
	receipt := Receipt{e.ID, e.Sequence}
	if len(p.identity.dedup) >= 128 {
		var key string
		var at time.Time
		for k, v := range p.identity.dedup {
			if key == "" || v.at.Before(at) {
				key = k
				at = v.at
			}
		}
		delete(p.identity.dedup, key)
	}
	// Global receipt capacity prevents thousands of inactive identities retaining
	// an otherwise multiplicative payload cache. Capacity eviction is documented.
	count := 0
	var oldestIdentity *identityRecord
	var oldestKey string
	var oldestAt time.Time
	for _, owner := range h.identities {
		for key, receipt := range owner.dedup {
			count++
			if oldestIdentity == nil || receipt.at.Before(oldestAt) {
				oldestIdentity, oldestKey, oldestAt = owner, key, receipt.at
			}
		}
	}
	if count >= 8192 && oldestIdentity != nil {
		delete(oldestIdentity.dedup, oldestKey)
	}
	p.identity.dedup[r.ClientMessageID] = accepted{receipt, now, text, r.Scope, room}
	p.identity.lastSeen = now
	h.lastHuman = &now
	h.metrics["accepted_events"]++
	h.revision++
	h.publishPresence()
	return success(receipt)
}
func eventSize(e Event) int { return len(e.Payload.Text) + len(e.Sender.Name) + 256 }
func (h *Hub) trim() {
	cutoff := time.Now().Add(-h.config.HistoryAge)
	for stream, events := range h.history {
		for len(events) > 0 && (len(events) > h.config.HistoryLimit || events[0].Timestamp.Before(cutoff)) {
			h.historyBytes -= eventSize(events[0])
			events = events[1:]
		}
		h.history[stream] = events
	}
	for h.historyBytes > h.config.HistoryBytes {
		oldest := ""
		var at time.Time
		for s, events := range h.history {
			if len(events) > 0 && (oldest == "" || events[0].Timestamp.Before(at)) {
				oldest = s
				at = events[0].Timestamp
			}
		}
		if oldest == "" {
			break
		}
		h.historyBytes -= eventSize(h.history[oldest][0])
		h.history[oldest] = h.history[oldest][1:]
	}
}
func (h *Hub) record(kind, room string, sender Sender, text string) Event {
	return h.recordReply(kind, room, sender, text, "")
}
func (h *Hub) recordReply(kind, room string, sender Sender, text, replyTo string) Event {
	stream := "global"
	scope := "global"
	if room != "" {
		stream = room
		scope = "room"
	}
	h.sequences[stream]++
	e := Event{ID: randomID(), Sequence: h.sequences[stream], Timestamp: time.Now(), Type: kind, Scope: scope, RoomID: room, ReplyTo: replyTo, Sender: sender, Payload: Payload{text}}
	h.history[stream] = append(h.history[stream], e)
	h.historyBytes += eventSize(e)
	h.trim()
	h.lastEvent = &e.Timestamp
	for p := range h.peers {
		if p.ready && (room == "" || p.room == room) {
			h.enqueue(p, "lab:event", e)
		}
	}
	return e
}
func (h *Hub) snapshot(p *Peer, r Request) Snapshot {
	h.trim()
	s := Snapshot{BootID: h.boot, Identity: p.identity.Identity, Presence: h.presence(), Rooms: Rooms, RoomID: p.room, Positions: map[string]uint64{}, History: []Event{}}
	streams := []string{"global"}
	if p.room != "" {
		streams = append(streams, p.room)
	}
	for _, stream := range streams {
		s.Positions[stream] = h.sequences[stream]
		events := h.history[stream]
		s.History = append(s.History, events...)
		if last, ok := r.Positions[stream]; ok {
			if len(events) > 0 && last+1 < events[0].Sequence || len(events) == 0 && last < h.sequences[stream] {
				s.Truncated = true
			}
		}
	}
	if r.BootID != "" && r.BootID != h.boot {
		s.Truncated = true
	}
	sort.Slice(s.History, func(i, j int) bool { return s.History[i].Timestamp.Before(s.History[j].Timestamp) })
	return s
}
func (h *Hub) status() Status {
	state := "ready"
	if !h.ready {
		state = "unavailable"
	} else if !h.config.SubmissionsEnabled {
		state = "degraded"
	}
	s := Status{SchemaVersion: 1, BootID: h.boot, Status: state, GeneratedAt: time.Now(), PresenceRevision: h.revision, LastHumanActivityAt: h.lastHuman, LastEventAt: h.lastEvent, Rooms: map[string]int{}, SubmissionsEnabled: h.config.SubmissionsEnabled}
	for _, room := range Rooms {
		s.Rooms[room] = 0
	}
	for p := range h.peers {
		s.Connections.Total++
		if p.kind == "go-client" {
			s.Connections.GoClient++
			if p.ready {
				s.BotReady = true
			}
		} else {
			s.Connections.Browser++
		}
		if p.room != "" {
			s.Rooms[p.room]++
		}
	}
	return s
}
func (h *Hub) presence() Presence {
	p := Presence{Status: h.status(), Participants: []Participant{}}
	seen := map[string]*Participant{}
	for q := range h.peers {
		if q.identity != nil {
			id := q.identity.PublicID
			if seen[id] == nil {
				seen[id] = &Participant{ID: id, Name: q.identity.DisplayName, Color: q.identity.AvatarColor}
			}
			seen[id].Connections++
		}
	}
	for _, person := range seen {
		p.Participants = append(p.Participants, *person)
	}
	sort.Slice(p.Participants, func(i, j int) bool { return p.Participants[i].Name < p.Participants[j].Name })
	return p
}
func (h *Hub) publishPresence() {
	p := h.presence()
	for q := range h.peers {
		if q.ready {
			h.enqueue(q, "lab:presence", p)
			h.enqueue(q, "lab:bot:status", map[string]bool{"ready": p.BotReady})
		}
	}
}
func (h *Hub) Status() Status { h.mu.Lock(); defer h.mu.Unlock(); return h.status() }
func (h *Hub) Metrics() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := map[string]any{}
	for k, v := range h.metrics {
		m[k] = v
	}
	s := h.status()
	m["connections"] = s.Connections
	m["rooms"] = s.Rooms
	m["botReady"] = s.BotReady
	m["historyBytes"] = h.historyBytes
	m["pendingEchoes"] = len(h.pending)
	pressure := 0
	for p := range h.peers {
		pressure += len(p.out)
	}
	m["outboundQueued"] = pressure
	return m
}
func (h *Hub) Shutdown() {
	h.mu.Lock()
	h.ready = false
	h.revision++
	h.publishPresence()
	h.mu.Unlock()
}
func (h *Hub) Tick(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trim()
	for p := range h.peers {
		if !p.ready && now.Sub(p.connectedAt) > 10*time.Second && !p.closed {
			p.closed = true
			close(p.stop)
			go p.wire.Close()
		}
	}
	for id, r := range h.pending {
		if now.After(r.deadline) {
			h.metrics["ack_timeouts"]++
			h.enqueue(r.origin, "lab:bot:result", Echo{CorrelationID: id, Error: "Go client response timed out"})
			delete(h.pending, id)
		}
	}
	for id, i := range h.identities {
		for key, receipt := range i.dedup {
			if now.Sub(receipt.at) > h.config.HistoryAge {
				delete(i.dedup, key)
			}
		}
		if i.active == 0 && now.Sub(i.lastSeen) > 24*time.Hour {
			delete(h.names, i.DisplayName)
			delete(h.identities, id)
		}
	}
}

// RejectPayload subjects malformed application input to the same connection budget.
func (h *Hub) RejectPayload(p *Peer) Ack {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.peers[p] || p.closed {
		return failure("DISCONNECTED", "Connection is no longer active")
	}
	if !p.controls.allow(time.Now(), 80, 10*time.Second) {
		p.closed = true
		close(p.stop)
		go p.wire.Close()
		return h.reject("RATE_LIMITED", "Too many invalid requests")
	}
	return h.reject("INVALID_PAYLOAD", "Expected a request object with valid fields within 4 KiB")
}
