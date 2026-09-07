// Package config reads Weir's configuration from the environment (spec §4).
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type App string

const (
	Radarr     App = "radarr"
	Sonarr     App = "sonarr"
	Lidarr     App = "lidarr"
	Readarr    App = "readarr"
	Prowlarr   App = "prowlarr"
	Qbit       App = "qbit"
	Bazarr     App = "bazarr"
	Jellyfin   App = "jellyfin"
	Jellyseerr App = "jellyseerr"
)

var AllApps = []App{Radarr, Sonarr, Lidarr, Readarr, Prowlarr, Qbit, Bazarr, Jellyfin, Jellyseerr}

type AppConfig struct{ URL, Key, User, Pass, PublicURL string }

type Cleaner struct {
	Mode                                  string
	Interval, StallAfter, SlowFor, MaxETA time.Duration
	SlowBelow                             int64
	Strikes, MaxActionsPerTitle           int
}

type Config struct {
	Listen, DataDir, MediaDir, PublicURL, NtfyURL, NtfyTopic, RulesAt, LogLevel string
	Apps                                                                        map[App]AppConfig
	Cleaner                                                                     Cleaner
}

func (c Config) Enabled(a App) bool { _, ok := c.Apps[a]; return ok }

func Load(getenv func(string) string) (Config, error) {
	var errs []error
	get := func(k, def string) string {
		if v := getenv("WEIR_" + k); v != "" {
			return v
		}
		return def
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := get(k, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("WEIR_%s: %w", k, err))
		}
		return d
	}
	num := func(k string, def int) int {
		v := get(k, "")
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("WEIR_%s: %w", k, err))
		}
		return n
	}

	c := Config{
		Listen: get("LISTEN", ":3004"), DataDir: get("DATA_DIR", "/config"), MediaDir: get("MEDIA_DIR", "/data"),
		PublicURL: strings.TrimRight(get("PUBLIC_URL", "https://ratholepi.tail98e0c3.ts.net:3004"), "/"),
		NtfyURL:   get("NTFY_URL", ""), NtfyTopic: get("NTFY_TOPIC", ""),
		RulesAt: get("RULES_AT", "04:10"), LogLevel: get("LOG_LEVEL", "info"),
		Apps: map[App]AppConfig{},
	}
	c.Cleaner = Cleaner{
		Mode: get("CLEANER_MODE", "report"), Interval: dur("CLEANER_INTERVAL", 5*time.Minute),
		StallAfter: dur("CLEANER_STALL_AFTER", 30*time.Minute), SlowFor: dur("CLEANER_SLOW_FOR", 20*time.Minute),
		MaxETA: dur("CLEANER_MAX_ETA", 48*time.Hour), Strikes: num("CLEANER_STRIKES", 3),
		MaxActionsPerTitle: num("CLEANER_MAX_ACTIONS_PER_TITLE", 3),
	}
	if b, err := parseBytes(get("CLEANER_SLOW_BELOW", "50KiB")); err != nil {
		errs = append(errs, fmt.Errorf("WEIR_CLEANER_SLOW_BELOW: %w", err))
	} else {
		c.Cleaner.SlowBelow = b
	}
	switch c.Cleaner.Mode {
	case "off", "report", "act":
	default:
		errs = append(errs, fmt.Errorf("WEIR_CLEANER_MODE: %q is not off, report or act", c.Cleaner.Mode))
	}
	for _, a := range AllApps {
		up := strings.ToUpper(string(a))
		url := get(up+"_URL", "")
		if url == "" {
			continue
		}
		ac := AppConfig{URL: strings.TrimRight(url, "/"), PublicURL: strings.TrimRight(get(up+"_PUBLIC_URL", ""), "/")}
		if a == Qbit {
			ac.User, ac.Pass = get("QBIT_USER", ""), get("QBIT_PASS", "")
			if ac.User == "" {
				errs = append(errs, errors.New("WEIR_QBIT_URL set but WEIR_QBIT_USER missing"))
			}
			if ac.Pass == "" {
				errs = append(errs, errors.New("WEIR_QBIT_URL set but WEIR_QBIT_PASS missing"))
			}
		} else if ac.Key = get(up+"_KEY", ""); ac.Key == "" {
			errs = append(errs, fmt.Errorf("WEIR_%s_URL set but WEIR_%s_KEY missing", up, up))
		}
		c.Apps[a] = ac
	}
	if (c.NtfyURL == "") != (c.NtfyTopic == "") {
		errs = append(errs, errors.New("WEIR_NTFY_URL and WEIR_NTFY_TOPIC must be set together"))
	}
	return c, errors.Join(errs...)
}

// parseBytes accepts "50KiB", "1MiB", "2GiB", "800" (bytes).
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}} {
		if strings.HasSuffix(s, u.suf) {
			mult, s = u.m, strings.TrimSuffix(s, u.suf)
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}
