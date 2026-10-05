package model

import (
	"fmt"
	"time"
)

func Preset(count int) Lab {
	if count < 1 {
		count = 8
	}
	if count > MaxONUs {
		count = MaxONUs
	}
	now := time.Now().UTC()
	l := Lab{Version: 1, ID: NewID("lab-"), Name: "Kampung fiber", Description: "An end-to-end GPON access lab. Real packets, observable failures.", Revision: 1, CreatedAt: now, UpdatedAt: now, Nodes: []Node{}, Links: []Link{}, Subscribers: []Subscriber{}, Faults: []Fault{}, Radius: RadiusConfig{Mode: "builtin", Address: "10.203.0.1", AuthPort: 1812, AccountingPort: 1813, Secret: Secret(), InterimSeconds: 60}}
	branches := (count + 63) / 64
	if count <= 32 {
		branches = 2
	}
	if count == 1 {
		branches = 1
	}
	per := (count + branches - 1) / branches
	columns := min(per, 8)
	width := float64(columns) * 210
	if width < 600 {
		width = 600
	}
	branchColumns := min(branches, 4)
	center := float64(branchColumns-1) * (width + 100) / 2
	l.Nodes = append(l.Nodes, DefaultNode("router", "router-1", "MikroTik · core", center, 0), DefaultNode("olt", "olt-1", "HSGQ · access", center, 160))
	l.Links = append(l.Links, Link{ID: "uplink-1", Source: "router-1", SourcePort: "lan1", Target: "olt-1", TargetPort: "uplink", Medium: "ethernet", LengthM: 3})
	idx := 0
	for b := 0; b < branches; b++ {
		x := float64(b%branchColumns) * (width + 100)
		y := float64(b/branchColumns) * 1700
		odfID := fmt.Sprintf("odf-%d", b+1)
		splitID := fmt.Sprintf("splitter-%d", b+1)
		odf := DefaultNode("odf", odfID, fmt.Sprintf("ODF · feeder %02d", b+1), x, y+320)
		ratio := 2
		for ratio < per {
			ratio *= 2
		}
		split := DefaultNode("splitter", splitID, fmt.Sprintf("Cabinet %02d · 1:%d", b+1, ratio), x, y+475)
		split.Config.SplitRatio = ratio
		l.Nodes = append(l.Nodes, odf, split)
		l.Links = append(l.Links, Link{ID: fmt.Sprintf("feeder-%d", b+1), Source: "olt-1", SourcePort: fmt.Sprintf("pon%d", b+1), Target: odfID, TargetPort: "in", Medium: "fiber", LengthM: 1800, LossDB: 0.5}, Link{ID: fmt.Sprintf("distribution-%d", b+1), Source: odfID, SourcePort: "out1", Target: splitID, TargetPort: "in", Medium: "fiber", LengthM: 700, LossDB: 0.5})
		for j := 0; j < per && idx < count; j++ {
			idx++
			id := fmt.Sprintf("onu-%04d", idx)
			onu := DefaultNode("onu", id, fmt.Sprintf("Rumah %03d", idx), x+float64(j%columns)*210-float64(columns-1)*105, y+690+float64(j/columns)*120)
			onu.Config.Serial = fmt.Sprintf("HSGQ%08X", idx)
			onu.Config.MAC = fmt.Sprintf("02:46:54:%02x:%02x:%02x", idx>>16, (idx>>8)&255, idx&255)
			l.Nodes = append(l.Nodes, onu)
			l.Links = append(l.Links, Link{ID: fmt.Sprintf("drop-%04d", idx), Source: splitID, SourcePort: fmt.Sprintf("out%d", j+1), Target: id, TargetPort: "pon", Medium: "fiber", LengthM: 120 + float64(j*17), LossDB: 0.5})
			l.Subscribers = append(l.Subscribers, Subscriber{ID: fmt.Sprintf("subscriber-%04d", idx), ONUID: id, Username: fmt.Sprintf("pelanggan%04d", idx), Password: Secret()[:12], Enabled: true, RateLimit: "1M/1M", Address: SubscriberAddress(idx - 1)})
		}
	}
	return l
}
