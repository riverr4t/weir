package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Rule struct {
	ID         int64
	Name       string
	Scope      string
	Enabled    bool
	Tag        string
	Conditions json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type RuleRun struct {
	ID              int64
	RuleID          int64
	Started         time.Time
	Finished        time.Time
	Matched, Tagged int
	Bytes           int64
	Note            string
}

type RuleMatch struct {
	RunID  int64
	ItemID int64
	Title  string
	Path   string
	Size   int64
	Reason string
}

var ErrNotFound = errors.New("not found")

func (s *Store) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scope, enabled, tag, conditions, created_at, updated_at FROM rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanRule(sc scanner) (Rule, error) {
	var r Rule
	var en int
	var cond, c, u string
	if err := sc.Scan(&r.ID, &r.Name, &r.Scope, &en, &r.Tag, &cond, &c, &u); err != nil {
		return r, err
	}
	r.Enabled = en == 1
	r.Conditions = json.RawMessage(cond)
	r.CreatedAt, r.UpdatedAt = parseTS(c), parseTS(u)
	return r, nil
}

func (s *Store) Rule(ctx context.Context, id int64) (Rule, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT id, name, scope, enabled, tag, conditions, created_at, updated_at FROM rules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) SaveRule(ctx context.Context, r Rule, now time.Time) (int64, error) {
	en := 0
	if r.Enabled {
		en = 1
	}
	if len(r.Conditions) == 0 {
		r.Conditions = json.RawMessage("[]")
	}
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO rules(name, scope, enabled, tag, conditions, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			r.Name, r.Scope, en, r.Tag, string(r.Conditions), ts(now), ts(now))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET name=?, scope=?, enabled=?, tag=?, conditions=?, updated_at=? WHERE id=?`,
		r.Name, r.Scope, en, r.Tag, string(r.Conditions), ts(now), r.ID)
	return r.ID, err
}

func (s *Store) SetRuleEnabled(ctx context.Context, id int64, enabled bool, now time.Time) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET enabled=?, updated_at=? WHERE id=?`, en, ts(now), id)
	return err
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id=?`, id)
	return err
}

// RecordRun stores a run and its matches in one transaction.
func (s *Store) RecordRun(ctx context.Context, run RuleRun, matches []RuleMatch) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO rule_runs(rule_id, started, finished, matched, bytes, tagged, note) VALUES (?,?,?,?,?,?,?)`,
		run.RuleID, ts(run.Started), ts(run.Finished), run.Matched, run.Bytes, run.Tagged, run.Note)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	for _, m := range matches {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rule_matches(run_id, item_id, title, path, size, reason) VALUES (?,?,?,?,?,?)`,
			id, m.ItemID, m.Title, m.Path, m.Size, m.Reason); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// LastRuns returns the most recent run per rule.
func (s *Store) LastRuns(ctx context.Context) (map[int64]RuleRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, rule_id, started, finished, matched, bytes, tagged, note FROM rule_runs r
		WHERE id = (SELECT MAX(id) FROM rule_runs WHERE rule_id = r.rule_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]RuleRun{}
	for rows.Next() {
		var r RuleRun
		var st, fi string
		if err := rows.Scan(&r.ID, &r.RuleID, &st, &fi, &r.Matched, &r.Bytes, &r.Tagged, &r.Note); err != nil {
			return nil, err
		}
		r.Started, r.Finished = parseTS(st), parseTS(fi)
		out[r.RuleID] = r
	}
	return out, rows.Err()
}

func (s *Store) Run(ctx context.Context, id int64) (RuleRun, []RuleMatch, error) {
	var r RuleRun
	var st, fi string
	err := s.db.QueryRowContext(ctx, `SELECT id, rule_id, started, finished, matched, bytes, tagged, note FROM rule_runs WHERE id=?`, id).
		Scan(&r.ID, &r.RuleID, &st, &fi, &r.Matched, &r.Bytes, &r.Tagged, &r.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil, ErrNotFound
	}
	if err != nil {
		return r, nil, err
	}
	r.Started, r.Finished = parseTS(st), parseTS(fi)
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, item_id, title, path, size, reason FROM rule_matches WHERE run_id=? ORDER BY size DESC`, id)
	if err != nil {
		return r, nil, err
	}
	defer rows.Close()
	var ms []RuleMatch
	for rows.Next() {
		var m RuleMatch
		if err := rows.Scan(&m.RunID, &m.ItemID, &m.Title, &m.Path, &m.Size, &m.Reason); err != nil {
			return r, nil, err
		}
		ms = append(ms, m)
	}
	return r, ms, rows.Err()
}

func (s *Store) Runs(ctx context.Context, ruleID int64, limit int) ([]RuleRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, rule_id, started, finished, matched, bytes, tagged, note FROM rule_runs WHERE rule_id=? ORDER BY id DESC LIMIT ?`, ruleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleRun
	for rows.Next() {
		var r RuleRun
		var st, fi string
		if err := rows.Scan(&r.ID, &r.RuleID, &st, &fi, &r.Matched, &r.Bytes, &r.Tagged, &r.Note); err != nil {
			return nil, err
		}
		r.Started, r.Finished = parseTS(st), parseTS(fi)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PruneRuns(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rule_runs WHERE started < ?`, ts(before))
	return err
}
