package lidarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/riverr4t/weir/internal/apps/arr"
)

func TestArtistsAndAlbums(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "lidarr")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := map[string]string{"/api/v1/artist": "artist.json", "/api/v1/album": "album.json"}[r.URL.Path]
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	c := arr.New(srv.URL, "k", "v1")
	as, err := Artists(c)(context.Background())
	if err != nil || len(as) == 0 || as[0].Name == "" || as[0].Statistics.SizeOnDisk == 0 {
		t.Fatalf("artists: %v %+v", err, as)
	}
	al, err := Albums(c)(context.Background())
	if err != nil || len(al) == 0 || al[0].Title == "" {
		t.Fatalf("albums: %v", err)
	}
}
