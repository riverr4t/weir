// Package radarr starts the Radarr pollers on top of the shared arr client.
package radarr

import (
	"context"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/arrapp"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

const app = "radarr"

func Start(ctx context.Context, cfg config.AppConfig, s *snapshot.Store, m *metrics.M, notify func(string, bool)) *arr.Client {
	c := arr.New(cfg.URL, cfg.Key, "v3")
	arrapp.StartCommon(ctx, app, c, &s.Radarr, m, notify)
	go poll.Run(ctx, arrapp.Spec(app, "library", 15*time.Minute, notify), &s.RadarrMovies, m, c.Movies)
	return c
}
