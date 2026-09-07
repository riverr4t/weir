package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckOKWhenHealthzIs200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	if err := healthcheck(srv.URL); err != nil {
		t.Fatalf("expected healthy, got %v", err)
	}
}

func TestHealthcheckFailsWhenDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	if err := healthcheck(srv.URL); err == nil {
		t.Fatal("expected error")
	}
}

func TestListenURL(t *testing.T) {
	for in, want := range map[string]string{"": "http://127.0.0.1:3004", ":3006": "http://127.0.0.1:3006", "0.0.0.0:80": "http://127.0.0.1:80"} {
		if got := listenURL(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
