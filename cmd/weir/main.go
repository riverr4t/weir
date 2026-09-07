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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/ntfy"
	"github.com/riverr4t/weir/internal/apps/qbit"
	"github.com/riverr4t/weir/internal/apps/radarr"
	"github.com/riverr4t/weir/internal/apps/sonarr"
	"github.com/riverr4t/weir/internal/cleaner"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
	"github.com/riverr4t/weir/internal/store"
	"github.com/riverr4t/weir/internal/web"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	m := metrics.New()
	db, err := store.Open(filepath.Join(cfg.DataDir, "weir.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	pub := ntfy.New(cfg.NtfyURL, cfg.NtfyTopic)
	coal := ntfy.NewCoalescer(pub, time.Minute, cfg.PublicURL)
	snap := &snapshot.Store{}
	arrs := map[string]*arr.Client{}
	if cfg.Enabled(config.Radarr) {
		arrs["radarr"] = radarr.Start(ctx, cfg.Apps[config.Radarr], snap, m, coal.Transition)
	}
	if cfg.Enabled(config.Sonarr) {
		arrs["sonarr"] = sonarr.Start(ctx, cfg.Apps[config.Sonarr], snap, m, coal.Transition)
	}
	var qb *qbit.Client
	deps := web.Deps{Cfg: cfg, Snap: snap, DB: db, M: m, Arrs: arrs}
	if cfg.Enabled(config.Qbit) {
		qc := cfg.Apps[config.Qbit]
		qb = qbit.New(qc.URL, qc.User, qc.Pass)
		deps.Qbit = qb
		go poll.Run(ctx, poll.Spec{App: "qbit", Kind: "sync", Interval: 2 * time.Second, Notify: coal.Transition}, &snap.Qbit, m, qb.Sync)
	}
	srv := web.New(deps)
	if qb != nil {
		cl := cleaner.New(cfg.Cleaner, snap, db, arrs, qb, pub, m, cfg.PublicURL)
		go cl.Run(ctx)
	}
	go ticker(ctx, srv, snap, m)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
				_ = db.Prune(ctx, time.Now().AddDate(0, 0, -90))
			}
		}
	}()

	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("weir listening", "addr", cfg.Listen, "version", version, "apps", len(cfg.Apps), "cleaner", cfg.Cleaner.Mode)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		coal.Flush(context.Background())
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(sd)
	}
}

// ticker pushes speeds every 2 s and the queue rows when they change, and
// refreshes the snapshot-age gauges every 15 s.
func ticker(ctx context.Context, srv *web.Server, snap *snapshot.Store, m *metrics.M) {
	fast := time.NewTicker(2 * time.Second)
	slow := time.NewTicker(15 * time.Second)
	defer fast.Stop()
	defer slow.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			srv.Hub.Broadcast("speeds", srv.SpeedsHTML())
			srv.Hub.Broadcast("flow", srv.FlowValue())
			if h := srv.RowsHTML(ctx); h != last {
				last = h
				srv.Hub.Broadcast("queue", h)
			}
		case <-slow.C:
			now := time.Now()
			for _, app := range []string{"radarr", "sonarr", "lidarr", "readarr"} {
				if c := snap.Arr(app); c != nil {
					m.SnapshotAge.WithLabelValues(app, "queue").Set(c.Queue.Get().Age(now).Seconds())
				}
			}
			m.SnapshotAge.WithLabelValues("qbit", "sync").Set(snap.Qbit.Get().Age(now).Seconds())
		}
	}
}

func setupLogging(level string) {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}
