package app

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"ftthlab/internal/engine"
	"ftthlab/internal/model"
)

// A CWMP reboot uses the same scoped ONU power-cycle as the local UI. An ACS
// cannot execute host commands, restart the helper, or reboot another device.
func (a *App) rebootFromACS(ctx context.Context, labID, onuID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	old, err := a.Store.Get(ctx, labID)
	if err != nil {
		return err
	}
	node, ok := old.Node(onuID)
	if !ok || node.Kind != "onu" || a.runtime(old).Phase != "running" {
		return fmt.Errorf("ONU runtime is not running")
	}
	next, err := a.Store.Get(ctx, labID)
	if err != nil {
		return err
	}
	for _, fault := range next.Faults {
		if fault.Kind == "power_off" && fault.TargetID == onuID {
			return fmt.Errorf("ONU is already powered off")
		}
	}
	faultID := model.NewID("fault-")
	next.Faults = append(next.Faults, model.Fault{ID: faultID, Kind: "power_off", TargetType: "node", TargetID: onuID, CreatedAt: time.Now().UTC()})
	if err = a.disconnectONUSession(ctx, old, onuID); err != nil {
		return err
	}
	if _, err = a.commit(ctx, old, next); err != nil {
		return err
	}
	a.event(labID, "acs", "info", onuID, "ACS requested an ONU power cycle")
	go a.finishReboot(labID, faultID)
	return nil
}

func matchesONUSession(row map[string]string, node model.Node, sub model.Subscriber, observedAddress string) bool {
	if row["name"] != sub.Username || row["service"] != "pppoe" || row[".id"] == "" {
		return false
	}
	if observedAddress != "" && row["address"] != observedAddress {
		return false
	}
	if node.Config.MAC != "" && !strings.EqualFold(strings.ReplaceAll(row["caller-id"], "-", ":"), node.Config.MAC) {
		return false
	}
	return true
}

// A short optical outage alone need not tear down PPP before LCP times out.
// A reboot explicitly removes only the matched ONU session on its owning CHR.
// This uses the ordinary authenticated RouterOS API, with no new helper powers.
func (a *App) disconnectONUSession(ctx context.Context, lab model.Lab, onuID string) error {
	node, ok := lab.Node(onuID)
	if !ok || node.Kind != "onu" {
		return fmt.Errorf("ONU not found")
	}
	router, ok := model.RouterFor(lab, onuID)
	if !ok {
		return nil
	}
	var sub model.Subscriber
	for _, candidate := range lab.Subscribers {
		if candidate.ONUID == onuID {
			sub = candidate
			break
		}
	}
	if sub.ID == "" {
		return nil
	}
	address := ""
	for _, s := range a.runtime(lab).Sessions {
		if s.ONUID == onuID {
			address = s.Address
			break
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client, err := engine.DialRouter(ctx, net.JoinHostPort(model.ManagementIPs(lab)[router.ID], "8728"), router.Config.Username, router.Config.Password)
	if err != nil {
		return fmt.Errorf("cannot disconnect ONU for reboot: RouterOS API unavailable")
	}
	defer client.Close()
	rows, err := client.RunArgsContext(ctx, []string{"/ppp/active/print", "?name=" + sub.Username, "=.proplist=.id,name,service,caller-id,address"})
	if err != nil {
		return fmt.Errorf("cannot inspect ONU session before reboot")
	}
	matched := ""
	for _, row := range rows.Re {
		if !matchesONUSession(row.Map, node, sub, address) {
			continue
		}
		if matched != "" {
			return fmt.Errorf("ONU session is ambiguous; refusing to disconnect multiple sessions")
		}
		matched = row.Map[".id"]
	}
	if matched == "" {
		if len(rows.Re) > 0 {
			return fmt.Errorf("active PPP session does not match this ONU's MAC/address")
		}
		return nil
	}
	if _, err = client.RunArgsContext(ctx, []string{"/ppp/active/remove", "=.id=" + matched}); err != nil {
		return fmt.Errorf("could not disconnect the ONU session for reboot")
	}
	return nil
}
