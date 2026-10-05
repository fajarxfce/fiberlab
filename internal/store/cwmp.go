package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) LoadCWMP(ctx context.Context, labID, onuID string) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, "SELECT body FROM cwmp_state WHERE lab_id=? AND onu_id=?", labID, onuID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return body, err
}

func (s *Store) SaveCWMP(ctx context.Context, labID, onuID string, body []byte) error {
	if len(body) > 64<<10 {
		return fmt.Errorf("CWMP state exceeds 64 KiB")
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO cwmp_state(lab_id,onu_id,body) VALUES(?,?,?) ON CONFLICT(lab_id,onu_id) DO UPDATE SET body=excluded.body", labID, onuID, body)
	return err
}
