// Package qbit talks to qBittorrent's WebUI API v2 using sync/maindata
// deltas so a 2-second poll costs almost nothing (spec §5).
package qbit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Torrent struct {
	Hash       string  `json:"-"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Progress   float64 `json:"progress"`
	DLSpeed    int64   `json:"dlspeed"`
	UPSpeed    int64   `json:"upspeed"`
	ETA        int64   `json:"eta"`
	NumSeeds   int     `json:"num_seeds"`
	NumLeechs  int     `json:"num_leechs"`
	Downloaded int64   `json:"downloaded"`
	Uploaded   int64   `json:"uploaded"`
	Size       int64   `json:"size"`
	AddedOn    int64   `json:"added_on"`
	Ratio      float64 `json:"ratio"`
	Tracker    string  `json:"tracker"`
	Category   string  `json:"category"`
}

func (t Torrent) Stalled() bool { return t.State == "stalledDL" }

type State struct {
	DLSpeed, UPSpeed int64
	Torrents         map[string]Torrent
	Version          string
}

type Tracker struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

type Client struct {
	base, user, pass string
	http             *http.Client
	mu               sync.Mutex
	rid              int64
	state            State
	loggedIn         bool
}

func New(baseURL, user, pass string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{base: strings.TrimRight(baseURL, "/") + "/api/v2", user: user, pass: pass,
		http: &http.Client{Timeout: 15 * time.Second, Jar: jar}, state: State{Torrents: map[string]Torrent{}}}
}

var errForbidden = errors.New("qbit: 403")

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "Ok." {
		return fmt.Errorf("qbit login: %s %q", resp.Status, strings.TrimSpace(string(b)))
	}
	c.loggedIn = true
	return nil
}

// call performs one request, logging in first if needed and once more on a 403.
func (c *Client) call(ctx context.Context, method, path string, form url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		if !c.loggedIn {
			if err := c.login(ctx); err != nil {
				return err
			}
		}
		err := c.once(ctx, method, path, form, out)
		if errors.Is(err, errForbidden) {
			c.loggedIn = false
			continue
		}
		return err
	}
	return errForbidden
}

func (c *Client) once(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	u := c.base + path
	if method == http.MethodGet && form != nil {
		u += "?" + form.Encode()
	} else if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		return errForbidden
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("qbit %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		b, err := io.ReadAll(resp.Body)
		*s = strings.TrimSpace(string(b))
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type mainData struct {
	Rid             int64                      `json:"rid"`
	FullUpdate      bool                       `json:"full_update"`
	Torrents        map[string]json.RawMessage `json:"torrents"`
	TorrentsRemoved []string                   `json:"torrents_removed"`
	ServerState     struct {
		DL *int64 `json:"dl_info_speed"`
		UP *int64 `json:"up_info_speed"`
	} `json:"server_state"`
}

// Sync applies the next delta and returns a deep copy of the merged state.
func (c *Client) Sync(ctx context.Context) (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Version == "" {
		var v string
		if err := c.call(ctx, http.MethodGet, "/app/version", nil, &v); err != nil {
			return State{}, err
		}
		c.state.Version = v
	}
	var md mainData
	if err := c.call(ctx, http.MethodGet, "/sync/maindata", url.Values{"rid": {strconv.FormatInt(c.rid, 10)}}, &md); err != nil {
		return State{}, err
	}
	if md.FullUpdate {
		c.state.Torrents = map[string]Torrent{}
	}
	for hash, raw := range md.Torrents {
		t := c.state.Torrents[hash] // partial updates only carry changed fields; start from the old value
		if err := json.Unmarshal(raw, &t); err != nil {
			return State{}, fmt.Errorf("torrent %s: %w", hash, err)
		}
		t.Hash = hash
		c.state.Torrents[hash] = t
	}
	for _, hash := range md.TorrentsRemoved {
		delete(c.state.Torrents, hash)
	}
	if md.ServerState.DL != nil {
		c.state.DLSpeed = *md.ServerState.DL
	}
	if md.ServerState.UP != nil {
		c.state.UPSpeed = *md.ServerState.UP
	}
	c.rid = md.Rid
	out := State{DLSpeed: c.state.DLSpeed, UPSpeed: c.state.UPSpeed, Version: c.state.Version,
		Torrents: make(map[string]Torrent, len(c.state.Torrents))}
	for h, t := range c.state.Torrents {
		out.Torrents[h] = t
	}
	return out, nil
}

func (c *Client) Trackers(ctx context.Context, hash string) ([]Tracker, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Tracker
	return out, c.call(ctx, http.MethodGet, "/torrents/trackers", url.Values{"hash": {hash}}, &out)
}

func (c *Client) Stop(ctx context.Context, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, http.MethodPost, "/torrents/stop", url.Values{"hashes": {hash}}, nil)
}

func (c *Client) Start(ctx context.Context, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, http.MethodPost, "/torrents/start", url.Values{"hashes": {hash}}, nil)
}
