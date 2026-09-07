// Package store is Weir's durable state: SQLite, one file, WAL (spec §6).
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

const timeLayout = time.RFC3339Nano

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for sibling packages' typed queries (rules, play state).
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&current)
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for i, name := range names {
		v := i + 1
		if v <= current {
			continue
		}
		sqlText, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sqlText)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTS(s string) time.Time { t, _ := time.Parse(timeLayout, s); return t }

type Strike struct {
	DownloadID, App, Title, Condition string
	Count                             int
	FirstSeen, LastSeen               time.Time
}

func (s *Store) AddStrike(ctx context.Context, downloadID, app, title, condition string, now time.Time) (int, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO strikes(download_id, app, title, condition, count, first_seen, last_seen, cleared_at)
		VALUES (?,?,?,?,1,?,?,NULL)
		ON CONFLICT(download_id, condition) DO UPDATE SET
		  count = CASE WHEN cleared_at IS NULL THEN count + 1 ELSE 1 END,
		  first_seen = CASE WHEN cleared_at IS NULL THEN first_seen ELSE excluded.first_seen END,
		  last_seen = excluded.last_seen, cleared_at = NULL, title = excluded.title, app = excluded.app`,
		downloadID, app, title, condition, ts(now), ts(now))
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count FROM strikes WHERE download_id=? AND condition=?`, downloadID, condition).Scan(&n)
	return n, err
}

func (s *Store) ClearStrikes(ctx context.Context, downloadID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE strikes SET cleared_at=? WHERE download_id=? AND cleared_at IS NULL`, ts(now), downloadID)
	return err
}

func (s *Store) Strikes(ctx context.Context) ([]Strike, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT download_id, app, title, condition, count, first_seen, last_seen
		FROM strikes WHERE cleared_at IS NULL ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Strike
	for rows.Next() {
		var st Strike
		var f, l string
		if err := rows.Scan(&st.DownloadID, &st.App, &st.Title, &st.Condition, &st.Count, &f, &l); err != nil {
			return nil, err
		}
		st.FirstSeen, st.LastSeen = parseTS(f), parseTS(l)
		out = append(out, st)
	}
	return out, rows.Err()
}

type Action struct {
	ID                                                int64
	At                                                time.Time
	Kind, App, Subject, Detail, Actor, Outcome, Error string
}

func (s *Store) LogAction(ctx context.Context, a Action) (int64, error) {
	if a.At.IsZero() {
		a.At = time.Now()
	}
	if a.Actor == "" {
		a.Actor = "weir"
	}
	if a.Detail == "" {
		a.Detail = "{}"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO actions(at, kind, app, subject, detail, actor, outcome, error) VALUES (?,?,?,?,?,?,?,?)`,
		ts(a.At), a.Kind, a.App, a.Subject, a.Detail, a.Actor, a.Outcome, a.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Actions(ctx context.Context, limit int) ([]Action, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, kind, app, subject, detail, actor, outcome, error
		FROM actions ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		var a Action
		var at string
		if err := rows.Scan(&a.ID, &at, &a.Kind, &a.App, &a.Subject, &a.Detail, &a.Actor, &a.Outcome, &a.Error); err != nil {
			return nil, err
		}
		a.At = parseTS(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActionsForTitleSince counts real removals for one title key (spec §7 cap).
func (s *Store) ActionsForTitleSince(ctx context.Context, titleKey string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions
		WHERE kind = 'cleaner.remove' AND at >= ? AND json_extract(detail, '$.titleKey') = ?`, ts(since), titleKey).Scan(&n)
	return n, err
}

func (s *Store) Prune(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM actions WHERE at < ?`, ts(before))
	return err
}
