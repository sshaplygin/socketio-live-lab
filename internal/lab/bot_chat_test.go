package lab

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBotChatRequiresHumanMessageAndAudience(t *testing.T) {
	c := DefaultConfig()
	c.BotToken = "test-bot-token"
	h := NewHub(c)
	a := peer(t, h, randomID())
	outside := peer(t, h, randomID())
	h.Handle(a, "lab:room:join", Request{RoomID: "developers"})
	bot, _ := h.Connect(&testWire{rooms: map[string]bool{}})
	defer h.Disconnect(bot)
	h.AuthenticateBot(bot, c.BotToken)
	h.Handle(bot, "lab:bot:ready", Request{})
	send := func(scope, room string) string {
		t.Helper()
		ack := h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Text: "hello", Scope: scope, RoomID: room})
		if !ack.OK {
			t.Fatal(ack)
		}
		return ack.Data.(Receipt).EventID
	}
	id := send("room", "developers")
	request := Request{ReplyTo: id, Text: "pong"}
	if got := h.Handle(a, "lab:bot:chat", request); got.OK || got.Error.Code != "FORBIDDEN" {
		t.Fatal("browser impersonated bot")
	}
	human := *h.Status().LastHumanActivityAt
	globalPosition := h.sequences["global"]
	ack := h.Handle(bot, "lab:bot:chat", request)
	if !ack.OK {
		t.Fatal(ack)
	}
	last := h.history["developers"][len(h.history["developers"])-1]
	if last.ReplyTo != id || last.Type != "bot" || last.Sender.Type != "go-client" || h.sequences["global"] != globalPosition {
		t.Fatal("reply lost its identity, target or room isolation")
	}
	if !h.Status().LastHumanActivityAt.Equal(human) {
		t.Fatal("bot manufactured human activity")
	}
	if retry := h.Handle(bot, "lab:bot:chat", request); !retry.OK || retry.Data != ack.Data {
		t.Fatal("reply retry created a duplicate")
	}
	next := send("room", "developers")
	if got := h.Handle(bot, "lab:bot:chat", Request{ReplyTo: next, Text: "again"}); got.OK || got.Error.Code != "RATE_LIMITED" {
		t.Fatal("bot response flood was not limited")
	}
	h.Handle(a, "lab:room:join", Request{RoomID: "general"})
	if got := h.Handle(bot, "lab:bot:chat", Request{ReplyTo: next, Text: "empty room"}); got.OK || got.Error.Code != "NO_AUDIENCE" {
		t.Fatal("bot replied to a room containing only itself")
	}
	private := send("room", "general")
	if got := h.Handle(bot, "lab:bot:chat", Request{ReplyTo: private, Text: "unrelated room"}); got.OK || got.Error.Code != "UNKNOWN_REQUEST" {
		t.Fatal("bot replied to a room it has not joined")
	}
	global := send("global", "")
	h.Disconnect(a)
	h.Disconnect(outside)
	if got := h.Handle(bot, "lab:bot:chat", Request{ReplyTo: global, Text: "nobody home"}); got.OK || got.Error.Code != "NO_AUDIENCE" {
		t.Fatal("bot chatted with no connected people")
	}
	h.config.SubmissionsEnabled = false
	if got := h.Handle(bot, "lab:bot:chat", request); got.OK || got.Error.Code != "DISABLED" {
		t.Fatal("emergency switch did not disable bot chat")
	}
}

func TestBotChatRejectsExpiredAndNonHumanSources(t *testing.T) {
	c := DefaultConfig()
	c.BotToken = "test-bot-token"
	h := NewHub(c)
	a := peer(t, h, randomID())
	bot, _ := h.Connect(&testWire{rooms: map[string]bool{}})
	defer h.Disconnect(bot)
	h.AuthenticateBot(bot, c.BotToken)
	h.Handle(bot, "lab:bot:ready", Request{})
	ack := h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Scope: "global", Text: "hello"})
	id := ack.Data.(Receipt).EventID
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := h.botChat(bot, Request{ReplyTo: id, Text: "late"}, time.Now().Add(11*time.Second)); got.OK || got.Error.Code != "UNKNOWN_REQUEST" {
		t.Fatal("stale reply accepted")
	}
	for _, kind := range []string{"bot", "system"} {
		e := h.record(kind, "", Sender{ID: "server", Name: "server", Type: "server"}, "not human")
		if got := h.botChat(bot, Request{ReplyTo: e.ID, Text: "loop"}, time.Now()); got.OK || got.Error.Code != "UNKNOWN_REQUEST" {
			t.Fatal("bot answered a nonhuman event")
		}
	}
}

func TestBotChatCommands(t *testing.T) {
	for command, expected := range map[string]string{"/ping": "pong!", "/help": "/quote", "/joke": "goroutine", "привет": "Hello from Go", "ordinary message": "”\n— ", "/quote": "”\n— ", "@go_echo_bot /quote": "”\n— ", "@go_echo_bot /ping": "pong!"} {
		response := BotChatText(Event{Sender: Sender{Name: "atomic_goroutine"}, Payload: Payload{Text: command}})
		if !strings.HasPrefix(response, "@atomic_goroutine ") || !strings.Contains(response, expected) {
			t.Errorf("unexpected response to %s: %s", command, response)
		}
	}
}

func TestExplicitCommandsBypassAutomaticReplyCooldown(t *testing.T) {
	c := DefaultConfig()
	c.BotToken = "test-bot-token"
	h := NewHub(c)
	a := peer(t, h, randomID())
	send := func(text string) Ack {
		return h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Text: text, Scope: "global"})
	}
	if got := send("/ping"); got.OK || got.Error.Code != "BOT_UNAVAILABLE" {
		t.Fatal("offline bot command was silently accepted")
	}
	bot, _ := h.Connect(&testWire{rooms: map[string]bool{}})
	defer h.Disconnect(bot)
	h.AuthenticateBot(bot, c.BotToken)
	h.Handle(bot, "lab:bot:ready", Request{})
	for _, text := range []string{"ordinary message", "/quote", "/ping", "@go_echo_bot /joke", "/help"} {
		message := send(text)
		if !message.OK {
			t.Fatal(message)
		}
		request := Request{ReplyTo: message.Data.(Receipt).EventID, Text: "reply"}
		reply := h.Handle(bot, "lab:bot:chat", request)
		if !reply.OK {
			t.Fatalf("command %q was suppressed by automatic reply cooldown: %+v", text, reply)
		}
		if retry := h.Handle(bot, "lab:bot:chat", request); !retry.OK || retry.Data != reply.Data {
			t.Fatal("command reply was not deduplicated")
		}
	}
	if got := send("/ping"); got.OK || got.Error.Code != "RATE_LIMITED" {
		t.Fatal("commands bypassed the sender's message limit")
	}
	h.Handle(a, "lab:room:join", Request{RoomID: "experiments"})
	if got := h.Handle(a, "lab:send", Request{ClientMessageID: randomID(), Text: "/quote", Scope: "room", RoomID: "experiments"}); got.OK || got.Error.Code != "BOT_WRONG_ROOM" {
		t.Fatal("command was silently accepted in a room without the bot")
	}
}

func TestBotCommandRecognition(t *testing.T) {
	for text, want := range map[string]bool{"/quote": true, " /PING \n": true, "@go_echo_bot /joke": true, "@go_echo_bot": true, "ping": true, "hello": false, "": false, "/pingpong": false, "say /quote": false} {
		if IsBotCommand(text) != want {
			t.Errorf("unexpected command recognition for %q", text)
		}
	}
}

func TestQuoteDeckCyclesAndPayloadLimit(t *testing.T) {
	var deck quoteDeck
	previous := ""
	for cycle := 0; cycle < 20; cycle++ {
		seen := map[string]bool{}
		for range scientistQuotes {
			reply := deck.next()
			if seen[reply] || reply == previous {
				t.Fatal("quote repeated within a cycle or at its boundary")
			}
			if !utf8.ValidString(reply) || utf8.RuneCountInString("@"+strings.Repeat("x", 64)+" "+reply) > 500 {
				t.Fatal("quote does not fit a chat reply with its recipient")
			}
			seen[reply], previous = true, reply
		}
	}
}

func TestQuoteDeckConcurrentReplies(t *testing.T) {
	var deck quoteDeck
	var wg sync.WaitGroup
	replies := make(chan string, len(scientistQuotes))
	for range scientistQuotes {
		wg.Add(1)
		go func() { defer wg.Done(); replies <- deck.next() }()
	}
	wg.Wait()
	close(replies)
	seen := map[string]bool{}
	for reply := range replies {
		if seen[reply] {
			t.Fatal("concurrent replies consumed the same quote")
		}
		seen[reply] = true
	}
}
