package lab

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNameStylesAndSharedIdentity(t *testing.T) {
	for style, words := range nameStyles {
		t.Run(style, func(t *testing.T) {
			h := NewHub(DefaultConfig())
			p, _ := h.Connect(&testWire{rooms: map[string]bool{}})
			defer h.Disconnect(p)
			id := randomID()
			if a := h.Handle(p, "lab:bootstrap", Request{VisitorID: id, NameStyle: "unknown"}); a.OK || a.Error.Code != "INVALID_NAME_STYLE" || p.ready {
				t.Fatal("invalid bootstrap style accepted")
			}
			if !h.Handle(p, "lab:bootstrap", Request{VisitorID: id, NameStyle: style}).OK {
				t.Fatal("style bootstrap failed")
			}
			other := peer(t, h, id)
			h.Handle(p, "lab:room:join", Request{RoomID: "developers"})
			before := p.identity.Identity
			if a := h.Handle(p, "lab:identity:shuffle", Request{NameStyle: "<script>"}); a.OK || a.Error.Code != "INVALID_NAME_STYLE" || p.identity.Identity != before {
				t.Fatal("invalid style mutated identity")
			}
			if !h.Handle(p, "lab:identity:shuffle", Request{}).OK || p.identity.NameStyle != style || other.identity != p.identity || p.identity.DisplayName == before.DisplayName {
				t.Fatal("shuffle lost style or shared identity")
			}
			if p.room != "developers" || p.identity.PublicID != before.PublicID || p.identity.AvatarColor != before.AvatarColor {
				t.Fatal("shuffle changed membership or identity ownership")
			}
			parts := strings.Split(p.identity.DisplayName, "_")
			if !strings.Contains("|"+strings.Join(words.prefixes, "|")+"|", "|"+parts[0]+"|") || !strings.Contains("|"+strings.Join(words.nouns, "|")+"|", "|"+parts[1]+"|") {
				t.Fatal("name does not belong to chosen dictionary")
			}
			if !h.Handle(other, "lab:identity:shuffle", Request{NameStyle: "terminal"}).OK || p.identity.NameStyle != "terminal" {
				t.Fatal("style selection did not reach sibling identity")
			}
			if h.names[before.DisplayName] != "" || h.names[p.identity.DisplayName] != id {
				t.Fatal("name reservation ownership broken")
			}
		})
	}
}

type testWire struct {
	mu     sync.Mutex
	rooms  map[string]bool
	closed bool
}

func (w *testWire) Emit(string, ...interface{}) {}
func (w *testWire) Join(rooms ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range rooms {
		w.rooms[r] = true
	}
}
func (w *testWire) Leave(r string) { w.mu.Lock(); defer w.mu.Unlock(); delete(w.rooms, r) }
func (w *testWire) Close() error   { w.mu.Lock(); defer w.mu.Unlock(); w.closed = true; return nil }
func peer(t *testing.T, h *Hub, id string) *Peer {
	t.Helper()
	p, err := h.Connect(&testWire{rooms: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if a := h.Handle(p, "lab:bootstrap", Request{VisitorID: id}); !a.OK {
		t.Fatal(a)
	}
	t.Cleanup(func() { h.Disconnect(p) })
	return p
}
func TestIdentityPresenceAndGenerations(t *testing.T) {
	h := NewHub(DefaultConfig())
	id := randomID()
	a := peer(t, h, id)
	b := peer(t, h, id)
	if h.Status().Connections.Total != 2 || a.identity != b.identity {
		t.Fatal("tabs must share identity and count separately")
	}
	name := a.identity.DisplayName
	h.Disconnect(a)
	h.Disconnect(a)
	if h.Status().Connections.Total != 1 {
		t.Fatal("duplicate disconnect changed presence")
	}
	c := peer(t, h, id)
	if c.identity.DisplayName != name {
		t.Fatal("identity changed after reconnect")
	}
	if !h.Handle(b, "lab:identity:shuffle", Request{}).OK || b.identity.DisplayName == name || c.identity.DisplayName != b.identity.DisplayName {
		t.Fatal("rename did not update all tabs")
	}
	h.Disconnect(a)
	if h.Status().Connections.Total != 2 {
		t.Fatal("old generation removed replacement")
	}
}
func TestIdentityCollisionsAndBoundedRetention(t *testing.T) {
	config := DefaultConfig()
	config.MaxIdentities = 2
	h := NewHub(config)
	h.mu.Lock()
	first := h.allocateName("one", "systems", []byte{0, 0})
	second := h.allocateName("two", "systems", []byte{0, 0})
	h.mu.Unlock()
	if first == second {
		t.Fatal("collision")
	}
	a := peer(t, h, randomID())
	b := peer(t, h, randomID())
	h.Disconnect(a)
	c := peer(t, h, randomID())
	if len(h.identities) > 2 || b.identity == c.identity {
		t.Fatal("identity budget or uniqueness broken")
	}
}
func TestRoomsSequencesDedupAndLimits(t *testing.T) {
	config := DefaultConfig()
	config.HistoryLimit = 3
	h := NewHub(config)
	a := peer(t, h, randomID())
	if h.Handle(a, "lab:room:join", Request{RoomID: "unknown"}).OK {
		t.Fatal("arbitrary room accepted")
	}
	for _, room := range []string{"general", "developers"} {
		if !h.Handle(a, "lab:room:join", Request{RoomID: room}).OK {
			t.Fatal("join failed")
		}
	}
	status := h.Status()
	if status.Rooms["general"] != 0 || status.Rooms["developers"] != 1 {
		t.Fatal("room counts incorrect")
	}
	global := h.sequences["global"]
	request := Request{ClientMessageID: randomID(), Text: "hello", Scope: "room", RoomID: "developers"}
	receipt := h.Handle(a, "lab:send", request)
	if !receipt.OK || h.sequences["global"] != global {
		t.Fatal("room consumed global sequence")
	}
	if retry := h.Handle(a, "lab:send", request); retry.Data != receipt.Data {
		t.Fatal("retry not deduplicated")
	}
	for i := 0; i < 4; i++ {
		h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Text: "hello", Scope: "global"})
	}
	if h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Text: "over limit", Scope: "global"}).Error.Code != "RATE_LIMITED" {
		t.Fatal("rate limit missing")
	}
	if len(h.history["global"]) > 3 {
		t.Fatal("history unbounded")
	}
	snapshot := h.Handle(a, "lab:resync", Request{Positions: map[string]uint64{"global": 0}}).Data.(Snapshot)
	if !snapshot.Truncated {
		t.Fatal("retention gap missing")
	}
	if h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Text: "no membership", Scope: "room", RoomID: "general"}).OK {
		t.Fatal("room isolation missing")
	}
}
func TestActivityBotAndEmergencySwitch(t *testing.T) {
	config := DefaultConfig()
	config.BotToken = "secret-for-test"
	h := NewHub(config)
	a := peer(t, h, randomID())
	h.Handle(a, "lab:room:join", Request{RoomID: "general"})
	h.Handle(a, "lab:probe", Request{})
	h.Handle(a, "lab:resync", Request{})
	if h.Status().LastHumanActivityAt != nil {
		t.Fatal("control activity counted")
	}
	bot, err := h.Connect(&testWire{rooms: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Disconnect(bot)
	if h.AuthenticateBot(bot, "wrong") || !h.AuthenticateBot(bot, config.BotToken) || h.Status().BotReady {
		t.Fatal("bot authentication/readiness")
	}
	if !h.Handle(bot, "lab:bot:ready", Request{}).OK || !h.Status().BotReady {
		t.Fatal("bot never ready")
	}
	response := h.Handle(a, "lab:bot:echo", Request{Text: "echo"})
	if !response.OK {
		t.Fatal(response)
	}
	human := *h.Status().LastHumanActivityAt
	id := response.Data.(map[string]string)["correlationId"]
	if !h.Handle(bot, "lab:bot:reply", Request{CorrelationID: id, Text: "echo"}).OK {
		t.Fatal("bot result failed")
	}
	if !h.Status().LastHumanActivityAt.Equal(human) {
		t.Fatal("bot changed human timestamp")
	}
	h.Disconnect(bot)
	if h.Status().BotReady || h.Status().Connections.GoClient != 0 {
		t.Fatal("stale bot presence")
	}
	h.config.SubmissionsEnabled = false
	if h.Handle(a, "lab:send", Request{Text: "blocked", ClientMessageID: randomID(), Scope: "global"}).Error.Code != "DISABLED" {
		t.Fatal("emergency switch missing")
	}
	if h.Status().Status != "degraded" {
		t.Fatal("readiness should be degraded")
	}
}
func TestRetentionAgeBudgetAndPendingTimeout(t *testing.T) {
	c := DefaultConfig()
	c.HistoryBytes = 600
	c.HistoryAge = time.Millisecond
	c.BotToken = "token"
	h := NewHub(c)
	a := peer(t, h, randomID())
	h.Handle(a, "lab:send", Request{Text: "message", Scope: "global", ClientMessageID: randomID()})
	if h.historyBytes > c.HistoryBytes {
		t.Fatal("memory budget exceeded")
	}
	time.Sleep(2 * time.Millisecond)
	h.Tick(time.Now())
	if h.historyBytes != 0 {
		t.Fatal("history TTL not enforced")
	}
	bot, _ := h.Connect(&testWire{rooms: map[string]bool{}})
	defer h.Disconnect(bot)
	h.AuthenticateBot(bot, "token")
	h.Handle(bot, "lab:bot:ready", Request{})
	h.Handle(a, "lab:bot:echo", Request{Text: "timeout"})
	h.Tick(time.Now().Add(6 * time.Second))
	if len(h.pending) != 0 || h.metrics["ack_timeouts"] != 1 {
		t.Fatal("pending echo leaked")
	}
}
func TestConcurrentPresenceAndRevisions(t *testing.T) {
	c := DefaultConfig()
	c.QueueSize = 256
	h := NewHub(c)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := h.Connect(&testWire{rooms: map[string]bool{}})
			if e != nil {
				return
			}
			h.Handle(p, "lab:bootstrap", Request{})
			h.Handle(p, "lab:room:join", Request{RoomID: "general"})
			h.Disconnect(p)
			h.Disconnect(p)
		}()
	}
	wg.Wait()
	if h.Status().Connections.Total != 0 || h.Status().Rooms["general"] != 0 || h.Status().PresenceRevision < 50 {
		t.Fatal("concurrent registry inconsistent")
	}
}
func TestAdmissionAndBootstrapDeadline(t *testing.T) {
	c := DefaultConfig()
	c.MaxClients = 1
	h := NewHub(c)
	wire := &testWire{rooms: map[string]bool{}}
	p, _ := h.Connect(wire)
	defer h.Disconnect(p)
	if _, err := h.Connect(wire); err == nil {
		t.Fatal("capacity not enforced")
	}
	h.Tick(time.Now().Add(11 * time.Second))
	if !p.closed {
		t.Fatal("uninitialized connection not expired")
	}
}

type blockedWire struct {
	started, released chan struct{}
	begin, end        sync.Once
}

func (w *blockedWire) Emit(string, ...interface{}) {
	w.begin.Do(func() { close(w.started) })
	<-w.released
}
func (w *blockedWire) Join(...string) {}
func (w *blockedWire) Leave(string)   {}
func (w *blockedWire) Close() error   { w.end.Do(func() { close(w.released) }); return nil }
func TestSlowConsumerCannotBlockOtherConnections(t *testing.T) {
	c := DefaultConfig()
	c.QueueSize = 4
	h := NewHub(c)
	slowWire := &blockedWire{started: make(chan struct{}), released: make(chan struct{})}
	slow, _ := h.Connect(slowWire)
	defer h.Disconnect(slow)
	defer slowWire.Close()
	h.Handle(slow, "lab:bootstrap", Request{})
	select {
	case <-slowWire.started:
	case <-time.After(time.Second):
		t.Fatal("delivery worker not started")
	}
	fast := peer(t, h, randomID())
	for i := 0; i < 20; i++ {
		h.mu.Lock()
		h.record("system", "", Sender{ID: "server", Name: "server", Type: "server"}, "test event")
		h.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slow.closed || fast.closed || h.metrics["broadcast_failures"] == 0 {
		t.Fatal("slow consumer was not isolated")
	}
}
