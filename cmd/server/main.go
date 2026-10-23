package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sshaplygin/socketio-live-lab/internal/lab"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func number(key string, fallback, min, max int) int {
	value := env(key, strconv.Itoa(fallback))
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		slog.Error("invalid numeric configuration", "key", key)
		os.Exit(1)
	}
	return n
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	c := lab.DefaultConfig()
	c.BotToken = os.Getenv("LAB_BOT_TOKEN")
	if c.BotToken != "" && len(c.BotToken) < 16 {
		slog.Error("LAB_BOT_TOKEN must contain at least 16 characters")
		os.Exit(1)
	}
	c.SubmissionsEnabled = env("LAB_SUBMISSIONS_ENABLED", "true") == "true"
	c.MaxClients = number("LAB_MAX_CLIENTS", 200, 1, 10000)
	c.MaxIdentities = number("LAB_MAX_IDENTITIES", 2000, c.MaxClients, 100000)
	c.HistoryLimit = number("LAB_HISTORY_LIMIT", 100, 1, 1000)
	c.HistoryBytes = number("LAB_HISTORY_BYTES", 1<<20, 4096, 64<<20)
	c.QueueSize = number("LAB_QUEUE_SIZE", 64, 4, 256)
	c.HistoryAge = time.Duration(number("LAB_HISTORY_SECONDS", 900, 1, 86400)) * time.Second
	server := lab.NewServer(c, strings.Split(env("LAB_ALLOWED_ORIGINS", "http://localhost:4321,http://127.0.0.1:4321"), ","), strings.Split(env("LAB_ALLOWED_HOSTS", "localhost:8080,127.0.0.1:8080"), ","), os.Getenv("LAB_METRICS_TOKEN"))
	if err := server.TrustProxies(strings.Split(os.Getenv("LAB_TRUSTED_PROXIES"), ",")); err != nil {
		slog.Error("invalid LAB_TRUSTED_PROXIES", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: env("LAB_ADDR", "127.0.0.1:8080"), Handler: server, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		if err := server.IO.Serve(); err != nil {
			slog.Error("socket server failed", "error", err)
			stop()
		}
	}()
	go func() {
		slog.Info("live lab listening", "address", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			server.Hub.Tick(now)
		case <-ctx.Done():
			server.Hub.Shutdown()
			_ = server.IO.Close()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdown)
			return
		}
	}
}
