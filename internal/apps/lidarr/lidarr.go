// Package lidarr starts the Lidarr pollers on top of the shared arr client.
package lidarr

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

const app = "lidarr"

type Artist struct {
	ID         int64       `json:"id"`
	Name       string      `json:"artistName"`
	Added      time.Time   `json:"added"`
	Monitored  bool        `json:"monitored"`
	Tags       []int64     `json:"tags"`
	Path       string      `json:"path"`
	Images     []arr.Image `json:"images"`
	Statistics struct {
		AlbumCount      int   `json:"albumCount"`
		TrackFileCount  int   `json:"trackFileCount"`
		TrackCount      int   `json:"trackCount"`
		TotalTrackCount int   `json:"totalTrackCount"`
		SizeOnDisk      int64 `json:"sizeOnDisk"`
	} `json:"statistics"`
}

type Album struct {
	ID          int64       `json:"id"`
	Title       string      `json:"title"`
	ArtistID    int64       `json:"artistId"`
	Monitored   bool        `json:"monitored"`
	ReleaseDate time.Time   `json:"releaseDate"`
	Images      []arr.Image `json:"images"`
	Artist      struct {
		Name string `json:"artistName"`
	} `json:"artist"`
	Statistics struct {
		TrackFileCount int   `json:"trackFileCount"`
		TrackCount     int   `json:"trackCount"`
		SizeOnDisk     int64 `json:"sizeOnDisk"`
	} `json:"statistics"`
}

func Artists(c *arr.Client) func(context.Context) ([]Artist, error) {
	return func(ctx context.Context) ([]Artist, error) {
		var out []Artist
		return out, c.Get(ctx, "/artist", &out)
	}
}

func Albums(c *arr.Client) func(context.Context) ([]Album, error) {
	return func(ctx context.Context) ([]Album, error) {
		var out []Album
		return out, c.Get(ctx, "/album", &out)
	}
}

type Cells struct {
	Artists snapshot.Cell[[]Artist]
	Albums  snapshot.Cell[[]Album]
}

func Start(ctx context.Context, cfg config.AppConfig, s *snapshot.Store, cells *Cells, m *metrics.M, notify func(string, bool)) *arr.Client {
	c := arr.New(cfg.URL, cfg.Key, "v1")
	arrapp.StartCommon(ctx, app, c, &s.Lidarr, m, notify)
	go poll.Run(ctx, arrapp.Spec(app, "library", 15*time.Minute, notify), &cells.Artists, m, Artists(c))
	go poll.Run(ctx, arrapp.Spec(app, "albums", 15*time.Minute, notify), &cells.Albums, m, Albums(c))
	return c
}
