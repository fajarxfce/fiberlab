package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ftthlab/internal/model"
)

func TestPersistenceAndOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := model.Preset(32)
	if err = s.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Get(ctx, l.ID)
	second, _ := s.Get(ctx, l.ID)
	first.Name = "Saved first"
	saved, err := s.Save(ctx, first, first.Revision)
	if err != nil || saved.Revision != 2 {
		t.Fatal(err)
	}
	second.Name = "Must not overwrite"
	if _, err = s.Save(ctx, second, second.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.Get(ctx, l.ID)
	if err != nil || restored.Name != "Saved first" || len(restored.Subscribers) != 32 {
		t.Fatalf("failed persistence: %v", err)
	}
	info, _ := os.Stat(filepath.Join(dir, "lab.db"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential-bearing database must be owner-only")
	}
}
func TestEventCursor(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, _ := s.AddEvent(ctx, model.Event{LabID: "lab-a", Kind: "test", Level: "info", Message: "one"})
	_, _ = s.AddEvent(ctx, model.Event{LabID: "lab-b", Kind: "test", Level: "info", Message: "other lab"})
	b, _ := s.AddEvent(ctx, model.Event{LabID: "lab-a", Kind: "test", Level: "info", Message: "two"})
	rows, err := s.Events(ctx, "lab-a", a.Seq)
	if err != nil || len(rows) != 1 || rows[0].Seq != b.Seq {
		t.Fatalf("cursor or lab isolation failed: %+v %v", rows, err)
	}
}

func TestCWMPPersistenceDoesNotOverwriteTopologyAndCascades(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := model.Preset(1)
	if err := s.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"parameters":{"ssid":"Saved by ACS"},"bootstrapped":true}`)
	if err := s.SaveCWMP(ctx, l.ID, "onu-0001", body); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCWMP(ctx, "missing-lab", "onu-0001", body); err == nil {
		t.Fatal("ACS state created without an owning lab")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadCWMP(ctx, l.ID, "onu-0001")
	if err != nil || string(got) != string(body) {
		t.Fatalf("ACS state lost after restart: %s %v", got, err)
	}
	stored, err := s.Get(ctx, l.ID)
	if err != nil || stored.Revision != l.Revision {
		t.Fatal("ACS state altered editor revision")
	}
	if err := s.Delete(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadCWMP(ctx, l.ID, "onu-0001"); err != nil || got != nil {
		t.Fatal("deleted lab left credential-bearing ACS state behind")
	}
}
