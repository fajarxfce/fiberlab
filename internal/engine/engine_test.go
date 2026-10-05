package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ftthlab/internal/model"
)

func cloneLab(t *testing.T, lab model.Lab) model.Lab {
	t.Helper()
	b, _ := json.Marshal(lab)
	var copy model.Lab
	if err := json.Unmarshal(b, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestStopCanCancelWhileStartupOwnsOperations(t *testing.T) {
	e := New(t.TempDir(), "", 1000, 1000, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	e.view.Phase = "booting"
	e.ops.Lock()
	go func() { <-ctx.Done(); e.ops.Unlock(); close(e.done) }()
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := e.Stop(stopCtx); err != nil {
		t.Fatalf("Stop deadlocked behind startup: %v", err)
	}
}

func TestRuntimeLockPreventsSecondHelperRecovery(t *testing.T) {
	e := New(t.TempDir(), "", 1000, 1000, nil)
	release, err := e.lockRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if unlock, err := e.lockRuntime(); err == nil {
		unlock()
		t.Fatal("second helper obtained active runtime lock")
	}
}

func TestLiveChangesPreserveRuntimeStructure(t *testing.T) {
	original := model.Preset(8)
	changed := cloneLab(t, original)
	changed.Nodes[0].Position.X += 10
	changed.Subscribers[0].Enabled = false
	if !SameStructure(original, changed) {
		t.Fatal("layout or billing changes required a rebuild")
	}
	changed.Subscribers[0].Password = "changed"
	if SameStructure(original, changed) {
		t.Fatal("client credential change accepted without restarting pppd")
	}
}

func TestProcessLogsRemainBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ppp.log")
	log, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if _, err = log.Write(make([]byte, 2048)); err != nil {
			t.Fatal(err)
		}
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{path, path + ".1"} {
		info, err := os.Stat(path)
		if err != nil || info.Size() > 128<<10 {
			t.Fatalf("unbounded process log: %v %v", info, err)
		}
	}
}
