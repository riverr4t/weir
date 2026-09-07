package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":3004" || c.DataDir != "/config" || c.MediaDir != "/data" {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.Cleaner.Mode != "report" || c.Cleaner.Interval != 5*time.Minute || c.Cleaner.StallAfter != 30*time.Minute ||
		c.Cleaner.SlowFor != 20*time.Minute || c.Cleaner.MaxETA != 48*time.Hour || c.Cleaner.SlowBelow != 50*1024 ||
		c.Cleaner.Strikes != 3 || c.Cleaner.MaxActionsPerTitle != 3 {
		t.Fatalf("cleaner defaults wrong: %+v", c.Cleaner)
	}
	if c.RulesAt != "04:10" || c.LogLevel != "info" || c.PublicURL != "https://ratholepi.tail98e0c3.ts.net:3004" {
		t.Fatalf("misc defaults wrong: %+v", c)
	}
	if len(c.Apps) != 0 {
		t.Fatalf("no apps expected, got %v", c.Apps)
	}
}

func TestAppsAndAllErrorsAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		"WEIR_RADARR_URL":   "http://radarr:7878",
		"WEIR_QBIT_URL":     "http://gluetun:8080",
		"WEIR_CLEANER_MODE": "maybe",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"WEIR_RADARR_KEY", "WEIR_QBIT_USER", "WEIR_QBIT_PASS", "WEIR_CLEANER_MODE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestEnabledAppsParsed(t *testing.T) {
	c, err := Load(env(map[string]string{
		"WEIR_SONARR_URL": "http://sonarr:8989/", "WEIR_SONARR_KEY": "k", "WEIR_SONARR_PUBLIC_URL": "https://h:8989",
		"WEIR_QBIT_URL": "http://gluetun:8080", "WEIR_QBIT_USER": "u", "WEIR_QBIT_PASS": "p",
		"WEIR_CLEANER_SLOW_BELOW": "1MiB", "WEIR_CLEANER_STRIKES": "5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled("sonarr") || c.Enabled("radarr") {
		t.Fatal("Enabled wrong")
	}
	if c.Apps["sonarr"].URL != "http://sonarr:8989" {
		t.Fatalf("url: %q", c.Apps["sonarr"].URL)
	}
	if c.Apps["qbit"].User != "u" || c.Cleaner.SlowBelow != 1<<20 || c.Cleaner.Strikes != 5 {
		t.Fatalf("parsed wrong: %+v %+v", c.Apps["qbit"], c.Cleaner)
	}
}
