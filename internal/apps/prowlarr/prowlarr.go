// Package prowlarr reads indexer health and stats (v1 API, arr-shaped).
package prowlarr

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

const app = "prowlarr"

type Indexer struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Enable   bool   `json:"enable"`
	Protocol string `json:"protocol"`
	Priority int    `json:"priority"`
	Privacy  string `json:"privacy"`
}

type IndexerStat struct {
	IndexerID           int64  `json:"indexerId"`
	IndexerName         string `json:"indexerName"`
	AverageResponseTime int    `json:"averageResponseTime"`
	NumberOfQueries     int    `json:"numberOfQueries"`
	NumberOfGrabs       int    `json:"numberOfGrabs"`
	NumberOfRssQueries  int    `json:"numberOfRssQueries"`
	NumberOfFailed      int    `json:"numberOfFailedQueries"`
	NumberOfFailedGrabs int    `json:"numberOfFailedGrabs"`
}

type Stats struct {
	Indexers []IndexerStat `json:"indexers"`
}

type Application struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Enable    bool   `json:"enable"`
	SyncLevel string `json:"syncLevel"`
}

type Cells struct {
	Status   snapshot.Cell[arr.SystemStatus]
	Health   snapshot.Cell[[]arr.HealthItem]
	Indexers snapshot.Cell[[]Indexer]
	Stats    snapshot.Cell[Stats]
	Apps     snapshot.Cell[[]Application]
}

func Start(ctx context.Context, cfg config.AppConfig, cells *Cells, m *metrics.M, notify func(string, bool)) *arr.Client {
	c := arr.New(cfg.URL, cfg.Key, "v1")
	go poll.Run(ctx, arrapp.Spec(app, "status", 5*time.Minute, notify), &cells.Status, m, c.SystemStatus)
	go poll.Run(ctx, arrapp.Spec(app, "health", 5*time.Minute, notify), &cells.Health, m, c.Health)
	go poll.Run(ctx, arrapp.Spec(app, "indexers", 5*time.Minute, notify), &cells.Indexers, m, func(ctx context.Context) ([]Indexer, error) {
		var out []Indexer
		return out, c.Get(ctx, "/indexer", &out)
	})
	go poll.Run(ctx, arrapp.Spec(app, "stats", 5*time.Minute, notify), &cells.Stats, m, func(ctx context.Context) (Stats, error) {
		var out Stats
		return out, c.Get(ctx, "/indexerstats", &out)
	})
	go poll.Run(ctx, arrapp.Spec(app, "apps", 5*time.Minute, notify), &cells.Apps, m, func(ctx context.Context) ([]Application, error) {
		var out []Application
		return out, c.Get(ctx, "/applications", &out)
	})
	return c
}
