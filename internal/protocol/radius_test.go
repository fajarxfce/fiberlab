package protocol

import (
	"context"
	"crypto/md5"
	"net"
	"testing"
	"time"

	"ftthlab/internal/model"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2866"
	"layeh.com/radius/rfc2869"
	"layeh.com/radius/vendors/mikrotik"
)

func access(l model.Lab, chap bool) *radius.Packet {
	p := radius.New(radius.CodeAccessRequest, []byte(l.Radius.Secret))
	s := l.Subscribers[0]
	_ = rfc2865.UserName_SetString(p, s.Username)
	if chap {
		challenge := []byte("deterministic-challenge")
		_ = rfc2865.CHAPChallenge_Set(p, challenge)
		h := md5.New()
		h.Write([]byte{9})
		h.Write([]byte(s.Password))
		h.Write(challenge)
		_ = rfc2865.CHAPPassword_Set(p, append([]byte{9}, h.Sum(nil)...))
	} else {
		_ = rfc2865.UserPassword_SetString(p, s.Password)
	}
	_ = SignMessageAuthenticator(p)
	return p
}
func TestRadiusPAPCHAPAndBillingPolicy(t *testing.T) {
	for _, chap := range []bool{false, true} {
		t.Run(map[bool]string{false: "PAP", true: "CHAP"}[chap], func(t *testing.T) {
			l := model.Preset(1)
			service := RadiusService{Lab: func() model.Lab { return l }}
			p := access(l, chap)
			raw, _ := p.Encode()
			response := service.Handle(raw)
			reply, err := radius.Parse(response, p.Secret)
			if err != nil || reply.Code != radius.CodeAccessAccept {
				t.Fatalf("authentication failed: %v", err)
			}
			if !radius.IsAuthenticResponse(response, raw, p.Secret) {
				t.Fatal("invalid response authenticator")
			}
			for i := 4; i < 20; i++ {
				response[i] = raw[i]
			}
			if !VerifyMessageAuthenticator(response, p.Secret) {
				t.Fatal("response Message-Authenticator is not RFC 2869 compatible")
			}
			if rfc2865.FramedIPAddress_Get(reply).String() != l.Subscribers[0].Address || mikrotik.MikrotikRateLimit_GetString(reply) != "1M/1M" {
				t.Fatal("address/rate attributes missing")
			}
			l.Subscribers[0].Enabled = false
			reply, err = radius.Parse(service.Handle(raw), p.Secret)
			if err != nil || reply.Code != radius.CodeAccessReject {
				t.Fatal("suspended account accepted")
			}
		})
	}
}
func TestRadiusRejectsTamperingAndSupportsFaults(t *testing.T) {
	l := model.Preset(1)
	service := RadiusService{Lab: func() model.Lab { return l }}
	p := access(l, false)
	raw, _ := p.Encode()
	bad := append([]byte(nil), raw...)
	bad[len(bad)-1] ^= 0xff
	if service.Handle(bad) != nil {
		t.Fatal("tampered Message-Authenticator was accepted")
	}
	if service.Handle([]byte{1, 2, 3}) != nil {
		t.Fatal("malformed packet accepted")
	}
	l.Faults = []model.Fault{{Kind: "radius_timeout"}}
	if service.Handle(raw) != nil {
		t.Fatal("timeout injection returned a response")
	}
	l.Faults = []model.Fault{{Kind: "radius_reject"}}
	reply, _ := radius.Parse(service.Handle(raw), p.Secret)
	if reply == nil || reply.Code != radius.CodeAccessReject {
		t.Fatal("reject injection failed")
	}
	l.Faults = nil
	p = radius.New(radius.CodeAccessRequest, []byte(l.Radius.Secret))
	_ = rfc2865.UserName_SetString(p, l.Subscribers[0].Username)
	_ = rfc2865.UserPassword_SetString(p, "incorrect")
	_ = SignMessageAuthenticator(p)
	raw, _ = p.Encode()
	reply, _ = radius.Parse(service.Handle(raw), p.Secret)
	if reply == nil || reply.Code != radius.CodeAccessReject {
		t.Fatal("wrong password accepted")
	}
}
func TestRadiusAccounting64BitAndAuthenticator(t *testing.T) {
	l := model.Preset(1)
	var got Accounting
	service := RadiusService{Lab: func() model.Lab { return l }, OnAccounting: func(a Accounting) { got = a }}
	p := radius.New(radius.CodeAccountingRequest, []byte(l.Radius.Secret))
	_ = rfc2865.UserName_SetString(p, l.Subscribers[0].Username)
	_ = rfc2866.AcctSessionID_SetString(p, "session-1")
	_ = rfc2866.AcctStatusType_Set(p, rfc2866.AcctStatusType_Value_InterimUpdate)
	_ = rfc2866.AcctInputOctets_Set(p, 55)
	_ = rfc2866.AcctOutputOctets_Set(p, 99)
	_ = rfc2869.AcctInputGigawords_Set(p, 1)
	_ = rfc2869.AcctOutputGigawords_Set(p, 2)
	_ = SignMessageAuthenticator(p)
	raw, _ := p.Encode()
	response := service.Handle(raw)
	reply, err := radius.Parse(response, p.Secret)
	if err != nil || reply.Code != radius.CodeAccountingResponse {
		t.Fatalf("accounting response failed: %v", err)
	}
	if got.Kind != "Interim" || got.TXBytes != (1<<32)+55 || got.RXBytes != (2<<32)+99 || got.SessionID != "session-1" {
		t.Fatalf("accounting counters or ID incorrect: %+v", got)
	}
	if !radius.IsAuthenticResponse(response, raw, p.Secret) {
		t.Fatal("invalid accounting response authenticator")
	}
	raw[5] ^= 1
	if service.Handle(raw) != nil {
		t.Fatal("invalid accounting authenticator accepted")
	}
}
func TestRadiusUDPLifecycle(t *testing.T) {
	l := model.Preset(1)
	s := &RadiusService{Lab: func() model.Lab { return l }, AllowedNAS: map[string]bool{"127.0.0.1": true}}
	if err := s.Listen("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reply, err := radius.Exchange(ctx, access(l, false), s.connections[0].LocalAddr().String())
	if err != nil || reply.Code != radius.CodeAccessAccept {
		t.Fatalf("real UDP exchange failed: %v", err)
	}
}
func TestDisconnectRequestUsesRFC5176(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:3799")
	if err != nil {
		t.Skip("test port occupied")
	}
	defer conn.Close()
	secret := "test-secret-123"
	done := make(chan error, 1)
	go func() {
		b := make([]byte, 4096)
		n, peer, err := conn.ReadFrom(b)
		if err != nil {
			done <- err
			return
		}
		if !radius.IsAuthenticRequest(b[:n], []byte(secret)) || !VerifyMessageAuthenticator(b[:n], []byte(secret)) {
			done <- context.Canceled
			return
		}
		p, err := radius.Parse(b[:n], []byte(secret))
		if err != nil {
			done <- err
			return
		}
		response, _ := p.Response(radius.CodeDisconnectACK).Encode()
		_, err = conn.WriteTo(response, peer)
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = Disconnect(ctx, "127.0.0.1", secret, "customer", "session-1"); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
