// Package arr is the shared client for Radarr, Sonarr, Lidarr and Readarr
// (Bookshelf). They share the API shape; only the version segment and the
// library endpoints differ.
package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL, Key, Version string
	HTTP                  *http.Client
}

func New(baseURL, key, version string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Key: key, Version: version,
		HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/"+c.Version+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Get is the generic read; app packages use it for their own endpoints.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) Queue(ctx context.Context) ([]QueueItem, error) {
	var page struct {
		Records []QueueItem `json:"records"`
	}
	err := c.Get(ctx, "/queue?page=1&pageSize=1000&includeUnknownMovieItems=true&includeUnknownSeriesItems=true&includeUnknownAuthorItems=true&includeUnknownArtistItems=true", &page)
	for i := range page.Records {
		// the arrs report torrent hashes in upper case; qBittorrent keys them in lower case
		page.Records[i].DownloadID = strings.ToLower(page.Records[i].DownloadID)
	}
	return page.Records, err
}

func (c *Client) Health(ctx context.Context) ([]HealthItem, error) {
	var out []HealthItem
	return out, c.Get(ctx, "/health", &out)
}

func (c *Client) SystemStatus(ctx context.Context) (SystemStatus, error) {
	var out SystemStatus
	return out, c.Get(ctx, "/system/status", &out)
}

func (c *Client) DiskSpace(ctx context.Context) ([]DiskSpace, error) {
	var out []DiskSpace
	return out, c.Get(ctx, "/diskspace", &out)
}

func (c *Client) Calendar(ctx context.Context, from, to time.Time) ([]CalendarItem, error) {
	var out []CalendarItem
	q := url.Values{"start": {from.UTC().Format("2006-01-02")}, "end": {to.UTC().Format("2006-01-02")}, "unmonitored": {"false"}}
	return out, c.Get(ctx, "/calendar?"+q.Encode(), &out)
}

func (c *Client) Movies(ctx context.Context) ([]Movie, error) {
	var out []Movie
	return out, c.Get(ctx, "/movie", &out)
}

func (c *Client) Series(ctx context.Context) ([]Series, error) {
	var out []Series
	return out, c.Get(ctx, "/series", &out)
}

func (c *Client) Episodes(ctx context.Context, seriesID int64) ([]Episode, error) {
	var out []Episode
	return out, c.Get(ctx, "/episode?seriesId="+strconv.FormatInt(seriesID, 10), &out)
}

func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var out []Tag
	return out, c.Get(ctx, "/tag", &out)
}

// WantedMissing returns one page of wanted/missing (or wanted/cutoff when cutoff is true).
func (c *Client) Wanted(ctx context.Context, cutoff bool, page, pageSize int) (Wanted, error) {
	var out Wanted
	kind := "missing"
	if cutoff {
		kind = "cutoff"
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}, "sortKey": {"airDateUtc"}, "sortDirection": {"descending"}, "monitored": {"true"}}
	if c.Version == "v3" && kind == "missing" {
		q.Set("includeSeries", "true")
	}
	return out, c.Get(ctx, "/wanted/"+kind+"?"+q.Encode(), &out)
}

// DeleteQueueItem removes a download that has NOT completed: the arr drops
// it from the client with its partial files, optionally blocklists the
// release, and (skipRedownload=false) searches again. This is the only
// removal Weir ever performs (spec §7). Callers must check
// QueueItem.Completed() first; the cleaner and the blocklist route do.
func (c *Client) DeleteQueueItem(ctx context.Context, id int64, blocklist bool) error {
	q := url.Values{"removeFromClient": {"true"}, "blocklist": {strconv.FormatBool(blocklist)}, "skipRedownload": {"false"}}
	return c.do(ctx, http.MethodDelete, "/queue/"+strconv.FormatInt(id, 10)+"?"+q.Encode(), nil, nil)
}

func (c *Client) Command(ctx context.Context, name string, body map[string]any) error {
	if body == nil {
		body = map[string]any{}
	}
	body["name"] = name
	b, _ := json.Marshal(body)
	return c.do(ctx, http.MethodPost, "/command", strings.NewReader(string(b)), nil)
}

// Post sends a JSON body; app packages use it for tag writes.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	return c.do(ctx, http.MethodPost, path, strings.NewReader(string(b)), out)
}

// Put sends a JSON body.
func (c *Client) Put(ctx context.Context, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	return c.do(ctx, http.MethodPut, path, strings.NewReader(string(b)), out)
}
