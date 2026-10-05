package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"ftthlab/internal/images"
	"ftthlab/internal/model"
)

type StartSpec struct {
	Lab          model.Lab    `json:"lab"`
	Image        images.Image `json:"image"`
	ControlURL   string       `json:"controlUrl"`
	ControlToken string       `json:"controlToken"`
}
type ExecSpec struct {
	NodeID  string `json:"nodeId"`
	Command string `json:"command"`
}
type CaptureSpec struct {
	LinkID  string `json:"linkId"`
	Seconds int    `json:"seconds"`
}
type Client struct {
	Socket string
	http   *http.Client
}

func NewClient(socket string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}
	return &Client{Socket: socket, http: &http.Client{Transport: transport, Timeout: 45 * time.Second}}
}
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	switch v := in.(type) {
	case StartSpec:
		v.Lab = model.NetworkLab(v.Lab)
		in = v
	case model.Lab:
		in = model.NetworkLab(v)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://netd"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("network helper unavailable; start ftthlab netd with sudo (%s)", c.Socket)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return nil
}
func (c *Client) Status(ctx context.Context) (model.RuntimeView, error) {
	var out model.RuntimeView
	err := c.Call(ctx, "GET", "/status", nil, &out)
	return out, err
}
func (c *Client) Capture(ctx context.Context, spec CaptureSpec) ([]byte, error) {
	b, _ := json.Marshal(spec)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://netd/capture", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var v struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&v)
		return nil, fmt.Errorf("%s", v.Error)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 9<<20))
}
