package app

import (
	"fmt"
	"net/http"

	"ftthlab/internal/engine"
	"ftthlab/internal/model"
)

func (a *App) startTraffic(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lab, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	var config model.TrafficConfig
	if !decode(w, r, &config) {
		return
	}
	if err := config.Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	known := make(map[string]bool, len(lab.Subscribers))
	for _, subscriber := range lab.Subscribers {
		known[subscriber.ONUID] = true
	}
	for _, id := range config.ONUIDs {
		if !known[id] {
			fail(w, 400, fmt.Errorf("%s is not an ONU subscriber in this lab", id))
			return
		}
	}
	view := a.runtime(lab)
	if view.Phase != "running" {
		fail(w, 409, fmt.Errorf("start this lab before generating traffic"))
		return
	}
	if view.Traffic != nil && view.Traffic.State == "running" {
		fail(w, 409, fmt.Errorf("traffic is already running; stop it before starting another run"))
		return
	}
	var next model.RuntimeView
	if err := a.Helper.Call(r.Context(), "POST", "/traffic/start", engine.TrafficSpec{LabID: lab.ID, TrafficConfig: config}, &next); err != nil {
		fail(w, 503, err)
		return
	}
	a.viewMu.Lock()
	a.view = next
	a.viewMu.Unlock()
	a.notify()
	write(w, 202, next)
}

func (a *App) stopTraffic(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lab, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	view := a.runtime(lab)
	if view.Traffic == nil || view.Traffic.State != "running" {
		write(w, 200, view)
		return
	}
	var next model.RuntimeView
	if err := a.Helper.Call(r.Context(), "POST", "/traffic/stop", map[string]string{"labId": lab.ID}, &next); err != nil {
		fail(w, 503, err)
		return
	}
	a.viewMu.Lock()
	a.view = next
	a.viewMu.Unlock()
	a.notify()
	write(w, 200, next)
}
