// Package traffic sends actual paced TCP payloads between ONU namespaces and
// the lab WAN. It never writes PPP, RouterOS, SNMP, or accounting counters.
package traffic

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"ftthlab/internal/model"
)

const Address = "198.18.0.1:8081"
const ioTimeout = 15 * time.Second

var payload [16 << 10]byte

// Server keeps one full-duplex connection per ONU. Buffers and connection
// counts are bounded; no file, child process, or third-party service is needed.
type Server struct {
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	clients  map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func Serve(listener net.Listener) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{listener: listener, ctx: ctx, cancel: cancel, clients: make(map[net.Conn]struct{})}
	s.wg.Add(1)
	go s.accept()
	return s
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil || len(s.clients) >= model.MaxONUs {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		s.clients[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			defer func() { s.mu.Lock(); delete(s.clients, conn); s.mu.Unlock() }()
			tune(conn)
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			var header [12]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil || string(header[:4]) != "FTL1" {
				return
			}
			down, up := int(binary.BigEndian.Uint32(header[4:8])), int(binary.BigEndian.Uint32(header[8:12]))
			if down > model.MaxTrafficKbps || up > model.MaxTrafficKbps || down+up == 0 {
				return
			}
			if _, err := conn.Write([]byte{1}); err != nil {
				return
			}
			_ = conn.SetDeadline(time.Time{})
			_ = exchange(s.ctx, conn, down, up)
		}()
	}
}

func (s *Server) Close() {
	s.cancel()
	s.listener.Close()
	s.mu.Lock()
	for conn := range s.clients {
		conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func tune(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(16 << 10)
		_ = tcp.SetWriteBuffer(16 << 10)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(5 * time.Second)
	}
}

func handshake(ctx context.Context, conn net.Conn, down, up int) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var header [12]byte
	copy(header[:4], "FTL1")
	binary.BigEndian.PutUint32(header[4:8], uint32(down))
	binary.BigEndian.PutUint32(header[8:12], uint32(up))
	if _, err := conn.Write(header[:]); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return err
	}
	if ack[0] != 1 {
		return fmt.Errorf("WAN traffic server rejected the connection")
	}
	return conn.SetDeadline(time.Time{})
}

// Both peers read continuously, even when their receive rate is disabled, so
// EOF still stops a unidirectional run. Cancellation closes blocked reads and
// writes immediately, including a blackholed optical path.
func exchange(parent context.Context, conn net.Conn, sendKbps, receiveKbps int) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	written := make(chan error, 1)
	go func() {
		err := pacedWrite(ctx, conn, sendKbps)
		written <- err
		cancel()
	}()
	buffer := make([]byte, 16<<10)
	var readErr error
	for {
		if receiveKbps > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(ioTimeout))
		}
		if _, readErr = conn.Read(buffer); readErr != nil {
			break
		}
	}
	cancel()
	conn.Close()
	writeErr := <-written
	if parent.Err() != nil {
		return parent.Err()
	}
	if writeErr != nil && writeErr != context.Canceled {
		return writeErr
	}
	return readErr
}

// Pace the sender, not the receiver: throttling reads would first fill kernel
// buffers with a burst that the requested rate and RouterOS graphs disagree on.
// Backpressure never earns catch-up bursts.
func pacedWrite(ctx context.Context, conn net.Conn, kbps int) error {
	if kbps == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	bytesPerSecond := kbps * 125
	chunk := min(len(payload), max(64, bytesPerSecond/4))
	interval := time.Duration(int64(chunk) * int64(time.Second) / int64(bytesPerSecond))
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		_ = conn.SetWriteDeadline(time.Now().Add(ioTimeout))
		if _, err := conn.Write(payload[:chunk]); err != nil {
			return err
		}
		timer.Reset(interval)
	}
}
