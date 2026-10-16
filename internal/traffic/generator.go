package traffic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"ftthlab/internal/model"
)

type Target struct {
	ONUID string
	Dial  func(context.Context) (net.Conn, error)
}

type Generator struct {
	mu      sync.Mutex
	current *run
}

type run struct {
	mu     sync.Mutex
	view   model.TrafficView
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func (g *Generator) Start(parent context.Context, config model.TrafficConfig, targets []Target) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if len(targets) != len(config.ONUIDs) {
		return fmt.Errorf("every selected ONU needs a network target")
	}
	for i, target := range targets {
		if target.ONUID != config.ONUIDs[i] || target.Dial == nil {
			return fmt.Errorf("invalid ONU network target")
		}
	}
	if err := parent.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current != nil {
		select {
		case <-g.current.done:
		default:
			return fmt.Errorf("traffic is already running; stop it before starting another run")
		}
	}
	config.ONUIDs = append([]string(nil), config.ONUIDs...)
	ctx, cancel := context.WithCancel(parent)
	now := time.Now().UTC()
	r := &run{ctx: ctx, cancel: cancel, done: make(chan struct{}), view: model.TrafficView{
		State: "running", Config: config, StartedAt: now, ONUs: make([]model.TrafficONU, len(targets)),
	}}
	if config.DurationSeconds > 0 {
		end := now.Add(time.Duration(config.DurationSeconds) * time.Second)
		r.view.EndsAt = &end
		cancel()
		r.ctx, r.cancel = context.WithDeadline(parent, end)
	}
	for i, target := range targets {
		r.view.ONUs[i] = model.TrafficONU{ONUID: target.ONUID, State: "waiting"}
	}
	g.current = r
	go r.execute(targets)
	return nil
}

func (r *run) execute(targets []Target) {
	defer close(r.done)
	defer r.cancel()
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Spread connection setup over one second for large labs.
			if !pause(r.ctx, time.Duration(i)*time.Second/time.Duration(len(targets))) {
				return
			}
			for r.ctx.Err() == nil {
				ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
				conn, err := target.Dial(ctx)
				cancel()
				if err == nil {
					tune(conn)
					err = handshake(r.ctx, conn, r.view.Config.DownloadKbps, r.view.Config.UploadKbps)
					if err == nil {
						r.status(i, "streaming", "")
						err = exchange(r.ctx, conn, r.view.Config.UploadKbps, r.view.Config.DownloadKbps)
					}
					conn.Close()
				}
				if r.ctx.Err() != nil {
					return
				}
				message := "Waiting for the ONU's PPPoE path to the WAN"
				if err != nil {
					message += ": " + err.Error()
				}
				r.status(i, "waiting", message)
				if !pause(r.ctx, 2*time.Second) {
					return
				}
			}
		}()
	}
	wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.view.State = "stopped"
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		r.view.State = "completed"
	}
	now := time.Now().UTC()
	r.view.StoppedAt = &now
	for i := range r.view.ONUs {
		r.view.ONUs[i].State = r.view.State
	}
}

func pause(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *run) status(i int, state, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.view.ONUs[i].State, r.view.ONUs[i].Error = state, message
}

func (g *Generator) Stop() {
	g.mu.Lock()
	r := g.current
	g.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}
}

func (g *Generator) Snapshot() *model.TrafficView {
	g.mu.Lock()
	r := g.current
	g.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	view := r.view
	view.Config.ONUIDs = append([]string(nil), view.Config.ONUIDs...)
	view.ONUs = append([]model.TrafficONU(nil), view.ONUs...)
	return &view
}
