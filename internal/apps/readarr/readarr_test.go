package readarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/riverr4t/weir/internal/apps/arr"
)

func TestAuthorsAndBooks(t *testing.T) {
	root := filepath.Join("..", "..", "..", "testdata", "fixtures", "readarr")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := map[string]string{"/api/v1/author": "author.json", "/api/v1/book": "book.json"}[r.URL.Path]
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	c := arr.New(srv.URL, "k", "v1")
	as, err := Authors(c)(context.Background())
	if err != nil || len(as) == 0 || as[0].Name == "" {
		t.Fatalf("authors: %v %+v", err, as)
	}
	bs, err := Books(c)(context.Background())
	if err != nil || len(bs) == 0 || bs[0].Title == "" {
		t.Fatalf("books: %v", err)
	}
}
