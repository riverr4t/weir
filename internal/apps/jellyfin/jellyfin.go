// Package jellyfin reads server info, sessions, counts and per-user play
// state (spec §5, §8).
package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arrapp"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

const app = "jellyfin"

type Client struct {
	base, key string
	http      *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token="%s", Client="weir", Device="weir", DeviceId="weir", Version="1"`, c.key))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("jellyfin %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Info struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
}

type Counts struct {
	MovieCount   int `json:"MovieCount"`
	SeriesCount  int `json:"SeriesCount"`
	EpisodeCount int `json:"EpisodeCount"`
	AlbumCount   int `json:"AlbumCount"`
	SongCount    int `json:"SongCount"`
	BookCount    int `json:"BookCount"`
}

type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

type Session struct {
	UserName       string    `json:"UserName"`
	UserID         string    `json:"UserId"`
	DeviceName     string    `json:"DeviceName"`
	Client         string    `json:"Client"`
	LastActivity   time.Time `json:"LastActivityDate"`
	NowPlayingItem *struct {
		Name         string `json:"Name"`
		Type         string `json:"Type"`
		SeriesName   string `json:"SeriesName"`
		RunTimeTicks int64  `json:"RunTimeTicks"`
	} `json:"NowPlayingItem"`
	PlayState struct {
		PositionTicks int64 `json:"PositionTicks"`
		IsPaused      bool  `json:"IsPaused"`
	} `json:"PlayState"`
}

// Item is one movie or series as one user sees it.
type Item struct {
	ID          string            `json:"Id"`
	Name        string            `json:"Name"`
	Type        string            `json:"Type"` // Movie | Series
	ProviderIds map[string]string `json:"ProviderIds"`
	DateCreated time.Time         `json:"DateCreated"`
	UserData    struct {
		Played         bool      `json:"Played"`
		PlayCount      int       `json:"PlayCount"`
		LastPlayedDate time.Time `json:"LastPlayedDate"`
		PlayedPct      float64   `json:"PlayedPercentage"`
	} `json:"UserData"`
}

// PlayState is the per-user view of the library: user id -> items.
type PlayState struct {
	Users []User
	Items map[string][]Item
}

type Cells struct {
	Info      snapshot.Cell[Info]
	Counts    snapshot.Cell[Counts]
	Sessions  snapshot.Cell[[]Session]
	PlayState snapshot.Cell[PlayState]
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	return out, c.do(ctx, http.MethodGet, "/System/Info", &out)
}

func (c *Client) Counts(ctx context.Context) (Counts, error) {
	var out Counts
	return out, c.do(ctx, http.MethodGet, "/Items/Counts", &out)
}

func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	return out, c.do(ctx, http.MethodGet, "/Sessions?ActiveWithinSeconds=900", &out)
}

func (c *Client) Users(ctx context.Context) ([]User, error) {
	var out []User
	return out, c.do(ctx, http.MethodGet, "/Users", &out)
}

func (c *Client) UserItems(ctx context.Context, userID string) ([]Item, error) {
	q := url.Values{"IncludeItemTypes": {"Movie,Series"}, "Recursive": {"true"}, "Fields": {"ProviderIds,DateCreated"}}
	var out struct {
		Items []Item `json:"Items"`
	}
	return out.Items, c.do(ctx, http.MethodGet, "/Users/"+userID+"/Items?"+q.Encode(), &out)
}

// PlayState fetches every user's view of movies and series.
func (c *Client) PlayState(ctx context.Context) (PlayState, error) {
	users, err := c.Users(ctx)
	if err != nil {
		return PlayState{}, err
	}
	ps := PlayState{Users: users, Items: map[string][]Item{}}
	for _, u := range users {
		items, err := c.UserItems(ctx, u.ID)
		if err != nil {
			return PlayState{}, fmt.Errorf("user %s: %w", u.Name, err)
		}
		ps.Items[u.ID] = items
	}
	return ps, nil
}

func (c *Client) RefreshLibrary(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/Library/Refresh", nil)
}

func Start(ctx context.Context, cfg config.AppConfig, cells *Cells, m *metrics.M, notify func(string, bool)) *Client {
	c := New(cfg.URL, cfg.Key)
	go poll.Run(ctx, arrapp.Spec(app, "info", 5*time.Minute, notify), &cells.Info, m, c.Info)
	go poll.Run(ctx, arrapp.Spec(app, "counts", 15*time.Minute, notify), &cells.Counts, m, c.Counts)
	go poll.Run(ctx, arrapp.Spec(app, "sessions", 15*time.Second, notify), &cells.Sessions, m, c.Sessions)
	go poll.Run(ctx, arrapp.Spec(app, "playstate", 15*time.Minute, notify), &cells.PlayState, m, c.PlayState)
	return c
}
