package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"ftthlab/internal/model"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("the topology changed; refresh before saving")
var ErrNotFound = errors.New("lab not found")

type Store struct{ db *sql.DB }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "lab.db")
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS labs(id TEXT PRIMARY KEY, revision INTEGER NOT NULL, body TEXT NOT NULL, updated_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS events(seq INTEGER PRIMARY KEY AUTOINCREMENT, lab_id TEXT NOT NULL, at TEXT NOT NULL, kind TEXT NOT NULL, level TEXT NOT NULL, subject TEXT NOT NULL, message TEXT NOT NULL); CREATE INDEX IF NOT EXISTS events_lab_seq ON events(lab_id,seq);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS cwmp_state(lab_id TEXT NOT NULL REFERENCES labs(id) ON DELETE CASCADE, onu_id TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(lab_id, onu_id));`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) List(ctx context.Context) ([]model.Lab, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM labs ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Lab{}
	for rows.Next() {
		var b string
		var l model.Lab
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, id string) (model.Lab, error) {
	var b string
	err := s.db.QueryRowContext(ctx, "SELECT body FROM labs WHERE id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Lab{}, ErrNotFound
	}
	var l model.Lab
	if err == nil {
		err = json.Unmarshal([]byte(b), &l)
	}
	return l, err
}
func (s *Store) Create(ctx context.Context, l model.Lab) error {
	if err := model.Validate(l); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO labs(id,revision,body,updated_at) VALUES(?,?,?,?)", l.ID, l.Revision, string(b), l.UpdatedAt.Format(time.RFC3339Nano))
	return err
}
func (s *Store) Save(ctx context.Context, l model.Lab, expected int64) (model.Lab, error) {
	if err := model.Validate(l); err != nil {
		return l, err
	}
	l.Revision = expected + 1
	l.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(l)
	if err != nil {
		return l, err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE labs SET revision=?,body=?,updated_at=? WHERE id=? AND revision=?", l.Revision, string(b), l.UpdatedAt.Format(time.RFC3339Nano), l.ID, expected)
	if err != nil {
		return l, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return l, ErrConflict
	}
	return l, nil
}
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM labs WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM events WHERE lab_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AddEvent(ctx context.Context, e model.Event) (model.Event, error) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	r, err := s.db.ExecContext(ctx, "INSERT INTO events(lab_id,at,kind,level,subject,message) VALUES(?,?,?,?,?,?)", e.LabID, e.At.Format(time.RFC3339Nano), e.Kind, e.Level, e.Subject, e.Message)
	if err != nil {
		return e, err
	}
	e.Seq, _ = r.LastInsertId()
	if e.Seq%100 == 0 {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM events WHERE lab_id=? AND seq NOT IN (SELECT seq FROM events WHERE lab_id=? ORDER BY seq DESC LIMIT 2000)", e.LabID, e.LabID)
	}
	return e, nil
}
func (s *Store) Events(ctx context.Context, id string, after int64) ([]model.Event, error) {
	query := "SELECT seq,lab_id,at,kind,level,subject,message FROM events WHERE lab_id=? AND seq>? ORDER BY seq DESC LIMIT 200"
	rows, err := s.db.QueryContext(ctx, query, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		var at string
		if err = rows.Scan(&e.Seq, &e.LabID, &at, &e.Kind, &e.Level, &e.Subject, &e.Message); err != nil {
			return nil, err
		}
		e.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("event timestamp: %w", err)
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
