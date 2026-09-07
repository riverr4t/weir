// Package bazarr reads subtitle state (Bazarr's own API, X-API-KEY header).
package bazarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arrapp"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

const app = "bazarr"

type Client struct {
	base, key string
	http      *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/") + "/api", key: key, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("bazarr %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Status struct {
	Version       string  `json:"bazarr_version"`
	SonarrVersion string  `json:"sonarr_version"`
	RadarrVersion string  `json:"radarr_version"`
	StartTime     float64 `json:"start_time"`
}

type Wanted struct {
	Total int          `json:"total"`
	Data  []WantedItem `json:"data"`
}

type WantedItem struct {
	Title       string `json:"title"`
	SeriesTitle string `json:"seriesTitle"`
	Episode     string `json:"episode_number"`
	Missing     []struct {
		Name string `json:"name"`
		Code string `json:"code2"`
	} `json:"missing_subtitles"`
}

type Provider struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Retry  string `json:"retry"`
}

type Cells struct {
	Status         snapshot.Cell[Status]
	MoviesWanted   snapshot.Cell[Wanted]
	EpisodesWanted snapshot.Cell[Wanted]
	Providers      snapshot.Cell[[]Provider]
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var out struct {
		Data Status `json:"data"`
	}
	return out.Data, c.get(ctx, "/system/status", &out)
}

func (c *Client) Wanted(ctx context.Context, kind string) (Wanted, error) {
	var out Wanted
	return out, c.get(ctx, "/"+kind+"/wanted?start=0&length=25", &out)
}

func (c *Client) Providers(ctx context.Context) ([]Provider, error) {
	var out struct {
		Data []Provider `json:"data"`
	}
	return out.Data, c.get(ctx, "/providers", &out)
}

func Start(ctx context.Context, cfg config.AppConfig, cells *Cells, m *metrics.M, notify func(string, bool)) *Client {
	c := New(cfg.URL, cfg.Key)
	go poll.Run(ctx, arrapp.Spec(app, "status", 5*time.Minute, notify), &cells.Status, m, c.Status)
	go poll.Run(ctx, arrapp.Spec(app, "movies-wanted", 5*time.Minute, notify), &cells.MoviesWanted, m, func(ctx context.Context) (Wanted, error) { return c.Wanted(ctx, "movies") })
	go poll.Run(ctx, arrapp.Spec(app, "episodes-wanted", 5*time.Minute, notify), &cells.EpisodesWanted, m, func(ctx context.Context) (Wanted, error) { return c.Wanted(ctx, "episodes") })
	go poll.Run(ctx, arrapp.Spec(app, "providers", 5*time.Minute, notify), &cells.Providers, m, c.Providers)
	return c
}
