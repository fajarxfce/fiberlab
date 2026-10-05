package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1
const MaxONUs = 500

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type NodeConfig struct {
	Powered        bool          `json:"powered"`
	AdminUp        bool          `json:"adminUp"`
	Registered     bool          `json:"registered"`
	Serial         string        `json:"serial,omitempty"`
	MAC            string        `json:"mac,omitempty"`
	VLAN           int           `json:"vlan"`
	ServiceVLANs   []int         `json:"serviceVlans,omitempty"`
	SplitRatio     int           `json:"splitRatio,omitempty"`
	TxDBm          float64       `json:"txDbm"`
	SensitivityDBm float64       `json:"sensitivityDbm"`
	Model          string        `json:"model,omitempty"`
	Username       string        `json:"username,omitempty"`
	Password       string        `json:"password,omitempty"`
	Community      string        `json:"community,omitempty"`
	MemoryMB       int           `json:"memoryMb,omitempty"`
	ACS            *ONUACSConfig `json:"acs,omitempty"`
}
type Node struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Label    string     `json:"label"`
	Position Position   `json:"position"`
	Config   NodeConfig `json:"config"`
}
type Link struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	SourcePort string  `json:"sourcePort"`
	Target     string  `json:"target"`
	TargetPort string  `json:"targetPort"`
	Medium     string  `json:"medium"`
	LengthM    float64 `json:"lengthM"`
	LossDB     float64 `json:"lossDb"`
}
type Subscriber struct {
	ID        string `json:"id"`
	ONUID     string `json:"onuId"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Enabled   bool   `json:"enabled"`
	RateLimit string `json:"rateLimit"`
	Address   string `json:"address"`
}
type RadiusConfig struct {
	Mode           string `json:"mode"`
	Address        string `json:"address"`
	AuthPort       int    `json:"authPort"`
	AccountingPort int    `json:"accountingPort"`
	Secret         string `json:"secret"`
	InterimSeconds int    `json:"interimSeconds"`
}
type Fault struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	Value      float64   `json:"value"`
	CreatedAt  time.Time `json:"createdAt"`
}
type Lab struct {
	Version     int          `json:"version"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Revision    int64        `json:"revision"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	Nodes       []Node       `json:"nodes"`
	Links       []Link       `json:"links"`
	Subscribers []Subscriber `json:"subscribers"`
	Radius      RadiusConfig `json:"radius"`
	Faults      []Fault      `json:"faults"`
	ImageID     string       `json:"imageId"`
	TrapAddress string       `json:"trapAddress,omitempty"`
	ACS         *ACSConfig   `json:"acs,omitempty"`
}
type Port struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Medium    string `json:"medium"`
	Direction string `json:"direction"`
}
type NodeState struct {
	ID            string   `json:"id"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	OpticalUp     bool     `json:"opticalUp"`
	ServicePathUp bool     `json:"servicePathUp"`
	RXDBm         *float64 `json:"rxDbm,omitempty"`
	OLTID         string   `json:"oltId,omitempty"`
	PON           string   `json:"pon,omitempty"`
	Path          []string `json:"path,omitempty"`
	ManagementIP  string   `json:"managementIp,omitempty"`
}
type Session struct {
	ONUID          string    `json:"onuId"`
	Username       string    `json:"username"`
	Status         string    `json:"status"`
	Address        string    `json:"address,omitempty"`
	Peer           string    `json:"peer,omitempty"`
	RXBytes        uint64    `json:"rxBytes"`
	TXBytes        uint64    `json:"txBytes"`
	UptimeSeconds  int64     `json:"uptimeSeconds"`
	SessionID      string    `json:"sessionId,omitempty"`
	LastAccounting string    `json:"lastAccounting,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type Event struct {
	Seq     int64     `json:"seq"`
	LabID   string    `json:"labId"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Level   string    `json:"level"`
	Subject string    `json:"subject"`
	Message string    `json:"message"`
}
type Metrics struct {
	ActiveSessions     int    `json:"activeSessions"`
	ConfiguredSessions int    `json:"configuredSessions"`
	RXBytes            uint64 `json:"rxBytes"`
	TXBytes            uint64 `json:"txBytes"`
	GoRSSBytes         uint64 `json:"goRssBytes"`
	WorkerRSSBytes     uint64 `json:"workerRssBytes"`
	QEMURSSBytes       uint64 `json:"qemuRssBytes"`
	PPPRSSBytes        uint64 `json:"pppRssBytes"`
}
type RuntimeView struct {
	LabID     string               `json:"labId"`
	RunID     string               `json:"runId"`
	Phase     string               `json:"phase"`
	Message   string               `json:"message"`
	StartedAt *time.Time           `json:"startedAt,omitempty"`
	Nodes     map[string]NodeState `json:"nodes"`
	Sessions  []Session            `json:"sessions"`
	Metrics   Metrics              `json:"metrics"`
	Progress  int                  `json:"progress"`
	Events    []Event              `json:"events,omitempty"`
	ACS       map[string]ACSStatus `json:"acs,omitempty"`
}
type Action struct {
	Kind     string `json:"kind"`
	TargetID string `json:"targetId"`
	Value    string `json:"value,omitempty"`
}

func NewID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func Secret() string { return NewID("") + NewID("") }
func DefaultNode(kind, id, label string, x, y float64) Node {
	c := NodeConfig{Powered: true, AdminUp: true, Registered: true, VLAN: 100, ServiceVLANs: []int{100}, SplitRatio: 8, TxDBm: 4, SensitivityDBm: -27, Community: "lab-read", MemoryMB: 1024}
	switch kind {
	case "router":
		c.Model = "RouterOS CHR v7"
		c.Username = "admin"
		c.Password = Secret()
	case "olt":
		c.Model = "HSGQ-G08R · reference"
		c.Username = "admin"
		c.Password = Secret()
	case "onu":
		c.Model = "GPON bridge + PPPoE client"
		c.TxDBm = 2.5
		c.Serial = "HSGQ" + strings.ToUpper(NewID("")[:8])
	case "splitter":
		c.Model = "Passive optical splitter"
	case "odp":
		c.Model = "Distribution point · integrated splitter"
	case "odf":
		c.Model = "Optical distribution frame"
	case "switch":
		c.Model = "802.1Q Ethernet bridge"
	}
	return Node{ID: id, Kind: kind, Label: label, Position: Position{x, y}, Config: c}
}
func Ports(n Node) []Port {
	var p []Port
	add := func(id, label, medium, direction string) { p = append(p, Port{id, label, medium, direction}) }
	switch n.Kind {
	case "router":
		for i := 1; i <= 4; i++ {
			add(fmt.Sprintf("lan%d", i), fmt.Sprintf("ether%d", i+2), "ethernet", "out")
		}
	case "switch":
		add("uplink", "UPLINK", "ethernet", "in")
		for i := 1; i <= 8; i++ {
			add(fmt.Sprintf("port%d", i), fmt.Sprintf("GE %d", i), "ethernet", "out")
		}
	case "olt":
		add("uplink", "UPLINK", "ethernet", "in")
		for i := 1; i <= 8; i++ {
			add(fmt.Sprintf("pon%d", i), fmt.Sprintf("PON %02d", i), "fiber", "out")
		}
	case "odf":
		add("in", "IN", "fiber", "in")
		add("out1", "OUT", "fiber", "out")
	case "splitter", "odp":
		add("in", "IN", "fiber", "in")
		for i := 1; i <= n.Config.SplitRatio; i++ {
			add(fmt.Sprintf("out%d", i), fmt.Sprint(i), "fiber", "out")
		}
	case "onu":
		add("pon", "PON", "fiber", "in")
	}
	return p
}
func (l Lab) Node(id string) (Node, bool) {
	for _, n := range l.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}
func (l Lab) Incoming(id string) (Link, bool) {
	for _, e := range l.Links {
		if e.Target == id {
			return e, true
		}
	}
	return Link{}, false
}
func (l Lab) SortedNodes(kind string) []Node {
	var n []Node
	for _, v := range l.Nodes {
		if v.Kind == kind {
			n = append(n, v)
		}
	}
	sort.Slice(n, func(i, j int) bool { return n[i].ID < n[j].ID })
	return n
}
func ManagementIPs(l Lab) map[string]string {
	out := map[string]string{}
	for i, n := range l.SortedNodes("router") {
		out[n.ID] = fmt.Sprintf("10.203.0.%d", 10+i)
	}
	for i, n := range l.SortedNodes("olt") {
		out[n.ID] = fmt.Sprintf("10.203.0.%d", 64+i)
	}
	return out
}
func SubscriberAddress(index int) string {
	return fmt.Sprintf("172.30.%d.%d", (index+10)/256, (index+10)%256)
}
func Round(v float64) float64 { return math.Round(v*100) / 100 }

var IDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
