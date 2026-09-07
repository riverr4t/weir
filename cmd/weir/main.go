// Command weir is the arr dashboard and queue cleaner for ratholepi.
// See docs/superpowers/specs/2026-09-06-weir-design.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/riverr4t/weir/internal/config"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(listenURL(os.Getenv("WEIR_LISTEN"))); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("weir exited", "err", err)
		os.Exit(1)
	}
}

// listenURL turns a bind address like ":3004" or "0.0.0.0:3004" into a loopback URL.
func listenURL(listen string) string {
	if listen == "" {
		listen = ":3004"
	}
	_, port, ok := strings.Cut(listen, ":")
	if !ok || port == "" {
		port = "3004"
	}
	return "http://127.0.0.1:" + port
}

func healthcheck(base string) error {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(base + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("weir listening", "addr", cfg.Listen, "version", version)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

func setupLogging(level string) {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}
