package lab

import (
	"crypto/subtle"
	"strings"
	"time"
	"unicode/utf8"
)

// Chat replies are authored by the actual Go client, then checked against a
// recent human event and a currently connected audience by the server.
func (h *Hub) botChat(p *Peer, r Request, now time.Time) Ack {
	if p.kind != "go-client" {
		return h.reject("FORBIDDEN", "Go client authentication required")
	}
	if !h.config.SubmissionsEnabled {
		return h.reject("DISABLED", "Interactive submissions are temporarily disabled")
	}
	text := strings.TrimSpace(r.Text)
	if !validID(r.ReplyTo) || text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 500 {
		return h.reject("INVALID_PAYLOAD", "A reply needs a message ID and 1–500 characters")
	}
	var source *Event
	for _, stream := range []string{"global", p.room} {
		for _, e := range h.history[stream] {
			if e.ReplyTo == r.ReplyTo && e.Type == "bot" {
				return success(Receipt{e.ID, e.Sequence})
			}
			if e.ID == r.ReplyTo && e.Type == "message" && e.Sender.Type == "browser" {
				copy := e
				source = &copy
			}
		}
	}
	if source == nil || now.Sub(source.Timestamp) > 10*time.Second {
		return h.reject("UNKNOWN_REQUEST", "Reply only to a recent human message in your room or Global")
	}
	audience := false
	for q := range h.peers {
		if q.kind == "browser" && q.ready && !q.closed && (source.RoomID == "" || q.room == source.RoomID) {
			audience = true
			break
		}
	}
	if !audience {
		return h.reject("NO_AUDIENCE", "No people remain in this chat")
	}
	if !IsBotCommand(source.Payload.Text) && !p.chatReplies.allow(now, 1, 5*time.Second) {
		return h.reject("RATE_LIMITED", "Chat replies are limited to one per five seconds")
	}
	e := h.recordReply("bot", source.RoomID, Sender{ID: "go_echo_bot", Name: "go_echo_bot", Type: "go-client", Color: "#288577"}, text, source.ID)
	return success(Receipt{e.ID, e.Sequence})
}

func botFirstWord(text string) string {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return ""
	}
	if words[0] == "@go_echo_bot" && len(words) > 1 {
		words = words[1:]
	}
	return words[0]
}

// Explicit requests use the sender's normal message limit, not the bot's
// cooldown for unsolicited replies to ordinary conversation.
func IsBotCommand(text string) bool {
	switch botFirstWord(text) {
	case "/quote", "/ping", "ping", "/joke", "/help", "@go_echo_bot":
		return true
	default:
		return false
	}
}

// BotChatText handles fixed commands and a local, rotating pool of sourced quotes.
func BotChatText(e Event) string {
	command := botFirstWord(e.Payload.Text)
	if command == "" {
		return ""
	}
	prefix := "@" + e.Sender.Name + " "
	switch command {
	case "/ping", "ping":
		return prefix + "pong! Your message reached a separate Go process and came back through the server."
	case "/help", "@go_echo_bot":
		return prefix + "Try /quote for a scientist's quote, /ping or /joke. Quote authors also appear in Docker's container names. Commands work in Everyone and Developers; automatic replies are limited to one every 5 seconds."
	case "/quote":
		return prefix + chatQuotes.next()
	case "/joke":
		return prefix + "A goroutine walks into a channel. It blocks. The bartender forgot to receive."
	case "hi", "hello", "hey", "привет":
		return prefix + "Hello from Go! Try /quote, /ping, /joke, or open a second window to exchange messages with yourself."
	default:
		return prefix + chatQuotes.next()
	}
}

func (h *Hub) AuthenticateBot(p *Peer, token string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.config.BotToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.config.BotToken)) != 1 {
		return false
	}
	for q := range h.peers {
		if q != p && q.kind == "go-client" {
			return false
		}
	}
	p.kind = "go-client"
	h.revision++
	h.publishPresence()
	return true
}
func (h *Hub) echo(p *Peer, r Request, now time.Time) Ack {
	if !h.config.SubmissionsEnabled {
		return h.reject("DISABLED", "Interactive submissions are temporarily disabled")
	}
	text := strings.TrimSpace(r.Text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 500 {
		return h.reject("INVALID_PAYLOAD", "Use 1–500 characters")
	}
	var bot *Peer
	for q := range h.peers {
		if q.kind == "go-client" && q.ready && !q.closed {
			bot = q
			break
		}
	}
	if bot == nil {
		return h.reject("BOT_UNAVAILABLE", "The Go client is offline")
	}
	if !p.echoes.allow(now, 2, 10*time.Second) {
		return h.reject("RATE_LIMITED", "Request up to two echoes per ten seconds")
	}
	for _, req := range h.pending {
		if req.origin == p {
			return h.reject("PENDING", "An echo request is already pending")
		}
	}
	if len(h.pending) >= h.config.MaxClients {
		return h.reject("CAPACITY", "Echo queue is full")
	}
	id := randomID()
	h.pending[id] = pendingEcho{p, bot, text, p.room, now.Add(5 * time.Second)}
	h.lastHuman = &now
	h.metrics["accepted_events"]++
	h.revision++
	h.publishPresence()
	h.enqueue(bot, "lab:bot:request", Echo{CorrelationID: id, Text: text})
	return success(map[string]string{"correlationId": id})
}
func (h *Hub) botReply(p *Peer, r Request) Ack {
	if p.kind != "go-client" {
		return h.reject("FORBIDDEN", "Go client authentication required")
	}
	req, ok := h.pending[r.CorrelationID]
	if !ok || req.bot != p {
		return h.reject("UNKNOWN_REQUEST", "Echo request expired")
	}
	if time.Now().After(req.deadline) {
		delete(h.pending, r.CorrelationID)
		h.metrics["ack_timeouts"]++
		h.enqueue(req.origin, "lab:bot:result", Echo{CorrelationID: r.CorrelationID, Error: "Go client response timed out"})
		return failure("TIMEOUT", "Echo expired")
	}
	if req.text != r.Text {
		return h.reject("INVALID_PAYLOAD", "Echo content does not match request")
	}
	delete(h.pending, r.CorrelationID)
	h.enqueue(req.origin, "lab:bot:result", Echo{CorrelationID: r.CorrelationID, Text: r.Text})
	h.record("bot", req.room, Sender{ID: "go_echo_bot", Name: "go_echo_bot", Type: "go-client", Color: "#288577"}, r.Text)
	return success(map[string]bool{"received": true})
}
