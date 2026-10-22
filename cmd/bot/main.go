package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/sshaplygin/go-socket.io/client"
	"github.com/sshaplygin/socketio-live-lab/internal/lab"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	token := os.Getenv("LAB_BOT_TOKEN")
	if len(token) < 16 {
		slog.Error("LAB_BOT_TOKEN must contain at least 16 characters")
		os.Exit(1)
	}
	endpoint := os.Getenv("LAB_SERVER_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A process-level retry also covers a backend that is absent at initial startup.
	for ctx.Err() == nil {
		cl, err := client.New(endpoint+lab.Namespace, client.WithHeader(http.Header{"Authorization": []string{"Bearer " + token}}), client.WithReconnection(true), client.WithReconnectionDelay(time.Second), client.WithReconnectionDelayMax(5*time.Second), client.WithMaxMessageSize(1<<20), client.WithWriteBufferSize(64), client.WithCloseTimeout(2*time.Second))
		if err != nil {
			slog.Error("invalid endpoint", "error", err)
			return
		}
		cl.OnEvent("lab:bot:request", func(c client.Conn, r lab.Echo) {
			c.Emit("lab:bot:reply", lab.Request{CorrelationID: r.CorrelationID, Text: r.Text})
		})
		var chatMu sync.Mutex
		var lastChat time.Time
		cl.OnEvent("lab:event", func(c client.Conn, e lab.Event) {
			if e.Type != "message" || e.Sender.Type != "browser" {
				return
			}
			chatMu.Lock()
			defer chatMu.Unlock()
			if !lab.IsBotCommand(e.Payload.Text) {
				if time.Since(lastChat) < 5*time.Second {
					return
				}
				lastChat = time.Now()
			}
			c.Emit("lab:bot:chat", lab.Request{ReplyTo: e.ID, Text: lab.BotChatText(e)}, func(a lab.Ack) {
				if !a.OK {
					slog.Warn("Go chat reply rejected", "error", a.Error)
				}
			})
		})
		cl.OnConnect(func(c client.Conn) error {
			c.Emit("lab:bot:ready", lab.Request{}, func(a lab.Ack) {
				if a.OK {
					slog.Info("Go echo client ready")
				} else {
					slog.Warn("Go echo client rejected")
					go c.Close()
				}
			})
			return nil
		})
		cl.OnDisconnect(func(_ client.Conn, reason string) { slog.Info("Go echo client disconnected", "reason", reason) })
		cl.OnError(func(_ client.Conn, err error) { slog.Warn("Go echo client error", "error", err.Error()) })
		if err = cl.Connect(); err == nil {
			<-ctx.Done()
		} else {
			slog.Warn("Go echo client connect failed", "error", err.Error())
		}
		_ = cl.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
