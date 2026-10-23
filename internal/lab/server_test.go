package lab

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	s := NewServer(DefaultConfig(), nil, nil, "")
	if err := s.TrustProxies([]string{"172.16.0.0/12", " ", ""}); err != nil {
		t.Fatal(err)
	}
	if err := s.TrustProxies([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
	for _, c := range []struct{ remote, xff, want string }{
		{"172.19.0.3:5000", "203.0.113.7", "203.0.113.7"},
		{"172.19.0.3:5000", "198.51.100.1, 203.0.113.7", "203.0.113.7"},
		{"172.19.0.3:5000", "2001:db8::1", "2001:db8::1"},
		{"172.19.0.3:5000", "", "172.19.0.3"},
		{"172.19.0.3:5000", "garbage", "172.19.0.3"},
		{"203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},
		{"[2001:db8::2]:5000", "198.51.100.1", "2001:db8::2"},
	} {
		r := httptest.NewRequest("GET", "/socket.io/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientIP(r); got != c.want {
			t.Errorf("remote %s xff %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestAdmissionLimitIsPerClientBehindProxy(t *testing.T) {
	c := DefaultConfig()
	c.MaxClients = 1000
	s := NewServer(c, nil, []string{"realtime.test"}, "")
	if err := s.TrustProxies([]string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	go s.IO.Serve()
	defer s.IO.Close()
	handshake := func(client string) int {
		r := httptest.NewRequest("GET", "/socket.io/?EIO=3&transport=polling", nil)
		r.Host = "realtime.test"
		r.RemoteAddr = "10.0.0.2:4000"
		r.Header.Set("X-Forwarded-For", client)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 60; i++ {
		if code := handshake("203.0.113.7"); code != http.StatusOK {
			t.Fatalf("handshake %d from the first client: %d", i+1, code)
		}
	}
	if code := handshake("203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("61st handshake from one client: %d, want 429", code)
	}
	if code := handshake("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("another client behind the same proxy was refused: %d", code)
	}
}
