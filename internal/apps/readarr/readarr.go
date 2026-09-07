// Package readarr starts the Readarr (Bookshelf) pollers on top of the shared arr client.
package readarr

import (
	"context"
	"time"

	"github.com/riverr4t/weir/internal/apps/arr"
	"github.com/riverr4t/weir/internal/apps/arrapp"
	"github.com/riverr4t/weir/internal/config"
	"github.com/riverr4t/weir/internal/metrics"
	"github.com/riverr4t/weir/internal/poll"
	"github.com/riverr4t/weir/internal/snapshot"
)

const app = "readarr"

type Author struct {
	ID         int64       `json:"id"`
	Name       string      `json:"authorName"`
	Added      time.Time   `json:"added"`
	Monitored  bool        `json:"monitored"`
	Tags       []int64     `json:"tags"`
	Path       string      `json:"path"`
	Images     []arr.Image `json:"images"`
	Statistics struct {
		BookCount     int   `json:"bookCount"`
		BookFileCount int   `json:"bookFileCount"`
		SizeOnDisk    int64 `json:"sizeOnDisk"`
	} `json:"statistics"`
}

type Book struct {
	ID          int64       `json:"id"`
	Title       string      `json:"title"`
	AuthorID    int64       `json:"authorId"`
	AuthorTitle string      `json:"authorTitle"`
	Monitored   bool        `json:"monitored"`
	Added       time.Time   `json:"added"`
	ReleaseDate time.Time   `json:"releaseDate"`
	Images      []arr.Image `json:"images"`
	Statistics  struct {
		BookFileCount int   `json:"bookFileCount"`
		SizeOnDisk    int64 `json:"sizeOnDisk"`
	} `json:"statistics"`
}

func Authors(c *arr.Client) func(context.Context) ([]Author, error) {
	return func(ctx context.Context) ([]Author, error) {
		var out []Author
		return out, c.Get(ctx, "/author", &out)
	}
}

func Books(c *arr.Client) func(context.Context) ([]Book, error) {
	return func(ctx context.Context) ([]Book, error) {
		var out []Book
		return out, c.Get(ctx, "/book", &out)
	}
}

type Cells struct {
	Authors snapshot.Cell[[]Author]
	Books   snapshot.Cell[[]Book]
}

func Start(ctx context.Context, cfg config.AppConfig, s *snapshot.Store, cells *Cells, m *metrics.M, notify func(string, bool)) *arr.Client {
	c := arr.New(cfg.URL, cfg.Key, "v1")
	arrapp.StartCommon(ctx, app, c, &s.Readarr, m, notify)
	go poll.Run(ctx, arrapp.Spec(app, "library", 15*time.Minute, notify), &cells.Authors, m, Authors(c))
	go poll.Run(ctx, arrapp.Spec(app, "books", 15*time.Minute, notify), &cells.Books, m, Books(c))
	return c
}
