// Package jellyseerr reads requests and performs request/approve/decline.
package jellyseerr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/apps/arrapp"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

const app = "jellyseerr"

type Client struct {
	base, key string
	http      *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/") + "/api/v1", key: key, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("jellyseerr %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Status struct {
	Version         string `json:"version"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

type Count struct {
	Total, Pending, Approved, Declined, Processing, Available, Movie, TV int
}

type Request struct {
	ID        int64     `json:"id"`
	Status    int       `json:"status"` // 1 pending, 2 approved, 3 declined
	Type      string    `json:"type"`   // movie | tv
	CreatedAt time.Time `json:"createdAt"`
	Media     struct {
		TmdbID int64  `json:"tmdbId"`
		TvdbID int64  `json:"tvdbId"`
		Status int    `json:"status"` // 5 available
		Type   string `json:"mediaType"`
	} `json:"media"`
	RequestedBy struct {
		ID             int64  `json:"id"`
		DisplayName    string `json:"displayName"`
		JellyfinUserID string `json:"jellyfinUserId"`
	} `json:"requestedBy"`
	Seasons []struct {
		SeasonNumber int `json:"seasonNumber"`
	} `json:"seasons"`
	Title string `json:"-"` // resolved by the page from the arr snapshot
}

type SearchResult struct {
	ID           int64  `json:"id"`
	MediaType    string `json:"mediaType"`
	Title        string `json:"title"`
	Name         string `json:"name"`
	ReleaseDate  string `json:"releaseDate"`
	FirstAirDate string `json:"firstAirDate"`
	PosterPath   string `json:"posterPath"`
	Overview     string `json:"overview"`
	MediaInfo    *struct {
		Status   int `json:"status"`
		Requests []struct {
			ID     int64 `json:"id"`
			Status int   `json:"status"`
		} `json:"requests"`
	} `json:"mediaInfo"`
}

func (r SearchResult) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

func (r SearchResult) Year() string {
	d := r.ReleaseDate
	if d == "" {
		d = r.FirstAirDate
	}
	if len(d) >= 4 {
		return d[:4]
	}
	return ""
}

// State summarises the search result: "here", "requested", or "".
func (r SearchResult) State() string {
	if r.MediaInfo == nil {
		return ""
	}
	if r.MediaInfo.Status == 5 {
		return "here"
	}
	if r.MediaInfo.Status >= 2 || len(r.MediaInfo.Requests) > 0 {
		return "requested"
	}
	return ""
}

type JUser struct {
	ID             int64  `json:"id"`
	DisplayName    string `json:"displayName"`
	JellyfinUserID string `json:"jellyfinUserId"`
}

type Cells struct {
	Status  snapshot.Cell[Status]
	Count   snapshot.Cell[Count]
	Pending snapshot.Cell[[]Request]
	Recent  snapshot.Cell[[]Request]
	Users   snapshot.Cell[[]JUser]
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	return out, c.do(ctx, http.MethodGet, "/status", nil, &out)
}

func (c *Client) Count(ctx context.Context) (Count, error) {
	var out struct {
		Total, Pending, Approved, Declined, Processing, Available, Movie, TV int
	}
	err := c.do(ctx, http.MethodGet, "/request/count", nil, &out)
	return Count(out), err
}

func (c *Client) Requests(ctx context.Context, filter string, take int) ([]Request, error) {
	var out struct {
		Results []Request `json:"results"`
	}
	q := url.Values{"take": {strconv.Itoa(take)}, "filter": {filter}, "sort": {"added"}}
	return out.Results, c.do(ctx, http.MethodGet, "/request?"+q.Encode(), nil, &out)
}

func (c *Client) Users(ctx context.Context) ([]JUser, error) {
	var out struct {
		Results []JUser `json:"results"`
	}
	return out.Results, c.do(ctx, http.MethodGet, "/user?take=100", nil, &out)
}

func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var out struct {
		Results []SearchResult `json:"results"`
	}
	err := c.do(ctx, http.MethodGet, "/search?"+url.Values{"query": {query}, "page": {"1"}}.Encode(), nil, &out)
	var keep []SearchResult
	for _, r := range out.Results {
		if r.MediaType == "movie" || r.MediaType == "tv" {
			keep = append(keep, r)
		}
	}
	return keep, err
}

func (c *Client) Request(ctx context.Context, mediaType string, mediaID int64) error {
	body := map[string]any{"mediaType": mediaType, "mediaId": mediaID}
	if mediaType == "tv" {
		body["seasons"] = "all"
	}
	return c.do(ctx, http.MethodPost, "/request", body, nil)
}

func (c *Client) Approve(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/request/"+strconv.FormatInt(id, 10)+"/approve", nil, nil)
}

func (c *Client) Decline(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/request/"+strconv.FormatInt(id, 10)+"/decline", nil, nil)
}

func Start(ctx context.Context, cfg config.AppConfig, cells *Cells, m *metrics.M, notify func(string, bool)) *Client {
	c := New(cfg.URL, cfg.Key)
	go poll.Run(ctx, arrapp.Spec(app, "status", 5*time.Minute, notify), &cells.Status, m, c.Status)
	go poll.Run(ctx, arrapp.Spec(app, "count", 30*time.Second, notify), &cells.Count, m, c.Count)
	go poll.Run(ctx, arrapp.Spec(app, "pending", 30*time.Second, notify), &cells.Pending, m, func(ctx context.Context) ([]Request, error) { return c.Requests(ctx, "pending", 50) })
	go poll.Run(ctx, arrapp.Spec(app, "recent", 2*time.Minute, notify), &cells.Recent, m, func(ctx context.Context) ([]Request, error) { return c.Requests(ctx, "all", 20) })
	go poll.Run(ctx, arrapp.Spec(app, "users", 15*time.Minute, notify), &cells.Users, m, c.Users)
	return c
}
