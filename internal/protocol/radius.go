package protocol

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"ftthlab/internal/model"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2866"
	"layeh.com/radius/rfc2869"
	"layeh.com/radius/vendors/mikrotik"
)

type Accounting struct {
	Username, SessionID, Kind, Address string
	RXBytes, TXBytes                   uint64
	Seconds                            uint32
	At                                 time.Time
}
type RadiusService struct {
	Lab          func() model.Lab
	OnAccounting func(Accounting)
	OnAuth       func(string, bool)
	AllowedNAS   map[string]bool
	mu           sync.Mutex
	connections  []net.PacketConn
	wg           sync.WaitGroup
}

func (s *RadiusService) Listen(auth, accounting string) error {
	for _, address := range []string{auth, accounting} {
		c, err := net.ListenPacket("udp4", address)
		if err != nil {
			s.Close()
			return err
		}
		s.mu.Lock()
		s.connections = append(s.connections, c)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.serve(c) }()
	}
	return nil
}
func (s *RadiusService) Close() {
	s.mu.Lock()
	for _, c := range s.connections {
		_ = c.Close()
	}
	s.connections = nil
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *RadiusService) serve(c net.PacketConn) {
	sem := make(chan struct{}, 32)
	buf := make([]byte, 4096)
	for {
		n, addr, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		host, _, _ := net.SplitHostPort(addr.String())
		if !s.AllowedNAS[host] {
			continue
		}
		b := append([]byte(nil), buf[:n]...)
		select {
		case sem <- struct{}{}:
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { <-sem }()
				response := s.Handle(b)
				if response != nil {
					_, _ = c.WriteTo(response, addr)
				}
			}()
		default:
		}
	}
}

// Handle processes the actual RADIUS wire format; malformed or unauthentic packets
// are silently discarded as required by the protocol.
func (s *RadiusService) Handle(raw []byte) []byte {
	l := s.Lab()
	secret := []byte(l.Radius.Secret)
	if len(raw) < 20 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(raw[2:4]))
	if length > len(raw) || length < 20 {
		return nil
	}
	raw = raw[:length]
	if !radius.IsAuthenticRequest(raw, secret) || !VerifyMessageAuthenticator(raw, secret) {
		return nil
	}
	p, err := radius.Parse(raw, secret)
	if err != nil {
		return nil
	}
	for _, f := range l.Faults {
		if f.Kind == "radius_timeout" {
			return nil
		}
	}
	username := rfc2865.UserName_GetString(p)
	var response *radius.Packet
	switch p.Code {
	case radius.CodeAccessRequest:
		var sub model.Subscriber
		found := false
		for _, v := range l.Subscribers {
			if v.Username == username {
				sub = v
				found = true
				break
			}
		}
		accepted := found && sub.Enabled
		for _, f := range l.Faults {
			if f.Kind == "radius_reject" {
				accepted = false
			}
		}
		if accepted {
			if chap := rfc2865.CHAPPassword_Get(p); len(chap) > 0 {
				if len(chap) != 17 {
					accepted = false
				} else {
					challenge := rfc2865.CHAPChallenge_Get(p)
					if len(challenge) == 0 {
						challenge = p.Authenticator[:]
					}
					h := md5.New()
					h.Write(chap[:1])
					h.Write([]byte(sub.Password))
					h.Write(challenge)
					accepted = subtle.ConstantTimeCompare(chap[1:], h.Sum(nil)) == 1
				}
			} else {
				password, err := rfc2865.UserPassword_LookupString(p)
				accepted = err == nil && subtle.ConstantTimeCompare([]byte(password), []byte(sub.Password)) == 1
			}
		}
		if accepted {
			response = p.Response(radius.CodeAccessAccept)
			_ = rfc2865.ServiceType_Set(response, rfc2865.ServiceType_Value_FramedUser)
			_ = rfc2865.FramedProtocol_Set(response, rfc2865.FramedProtocol_Value_PPP)
			_ = rfc2865.FramedIPAddress_Set(response, net.ParseIP(sub.Address))
			_ = mikrotik.MikrotikRateLimit_SetString(response, sub.RateLimit)
			_ = rfc2869.AcctInterimInterval_Set(response, rfc2869.AcctInterimInterval(l.Radius.InterimSeconds))
		} else {
			response = p.Response(radius.CodeAccessReject)
			_ = rfc2865.ReplyMessage_SetString(response, "Subscriber disabled or invalid credentials")
		}
		if s.OnAuth != nil {
			s.OnAuth(username, accepted)
		}
	case radius.CodeAccountingRequest:
		status := rfc2866.AcctStatusType_Get(p)
		kind := ""
		switch status {
		case rfc2866.AcctStatusType_Value_Start:
			kind = "Start"
		case rfc2866.AcctStatusType_Value_Stop:
			kind = "Stop"
		case rfc2866.AcctStatusType_Value_InterimUpdate:
			kind = "Interim"
		case rfc2866.AcctStatusType_Value_AccountingOn:
			kind = "Accounting-On"
		case rfc2866.AcctStatusType_Value_AccountingOff:
			kind = "Accounting-Off"
		}
		if kind != "" && s.OnAccounting != nil {
			s.OnAccounting(Accounting{Username: username, SessionID: rfc2866.AcctSessionID_GetString(p), Kind: kind, Address: rfc2865.FramedIPAddress_Get(p).String(), RXBytes: uint64(rfc2866.AcctOutputOctets_Get(p)) + (uint64(rfc2869.AcctOutputGigawords_Get(p)) << 32), TXBytes: uint64(rfc2866.AcctInputOctets_Get(p)) + (uint64(rfc2869.AcctInputGigawords_Get(p)) << 32), Seconds: uint32(rfc2866.AcctSessionTime_Get(p)), At: time.Now().UTC()})
		}
		response = p.Response(radius.CodeAccountingResponse)
	default:
		return nil
	}
	// RFC 2869: MAC the response with the original request authenticator before
	// computing its response authenticator. Include it even for legacy PAP NASes.
	if err = SignMessageAuthenticator(response); err != nil {
		return nil
	}
	encoded, err := response.Encode()
	if err != nil {
		return nil
	}
	return encoded
}

func VerifyMessageAuthenticator(raw, secret []byte) bool {
	if len(raw) < 20 {
		return false
	}
	copyRaw := append([]byte(nil), raw...)
	var received []byte
	found := false
	for i := 20; i < len(copyRaw); {
		if i+2 > len(copyRaw) {
			return false
		}
		length := int(copyRaw[i+1])
		if length < 2 || i+length > len(copyRaw) {
			return false
		}
		if copyRaw[i] == 80 {
			if found || length != 18 {
				return false
			}
			found = true
			received = append([]byte(nil), copyRaw[i+2:i+18]...)
			clear(copyRaw[i+2 : i+18])
		}
		i += length
	}
	if !found {
		return true
	}
	if radius.Code(copyRaw[0]) == radius.CodeAccountingRequest || radius.Code(copyRaw[0]) == radius.CodeDisconnectRequest || radius.Code(copyRaw[0]) == radius.CodeCoARequest {
		clear(copyRaw[4:20])
	}
	h := hmac.New(md5.New, secret)
	h.Write(copyRaw)
	return hmac.Equal(received, h.Sum(nil))
}
func SignMessageAuthenticator(p *radius.Packet) error {
	if err := rfc2869.MessageAuthenticator_Set(p, make([]byte, 16)); err != nil {
		return err
	}
	b, err := p.MarshalBinary()
	if err != nil {
		return err
	}
	if p.Code == radius.CodeAccountingRequest || p.Code == radius.CodeDisconnectRequest || p.Code == radius.CodeCoARequest {
		clear(b[4:20])
	}
	h := hmac.New(md5.New, p.Secret)
	h.Write(b)
	return rfc2869.MessageAuthenticator_Set(p, h.Sum(nil))
}
func Disconnect(ctx context.Context, address, secret, username, sessionID string) error {
	p := radius.New(radius.CodeDisconnectRequest, []byte(secret))
	_ = rfc2865.UserName_SetString(p, username)
	if sessionID != "" {
		_ = rfc2866.AcctSessionID_SetString(p, sessionID)
	}
	_ = SignMessageAuthenticator(p)
	c := radius.Client{Retry: time.Second, MaxPacketErrors: 3}
	target := address
	if _, _, err := net.SplitHostPort(address); err != nil {
		target = net.JoinHostPort(address, "3799")
	}
	response, err := c.Exchange(ctx, p, target)
	if err != nil {
		return fmt.Errorf("Disconnect-Request: %w", err)
	}
	if response.Code != radius.CodeDisconnectACK {
		return fmt.Errorf("NAS returned %s", response.Code)
	}
	return nil
}
