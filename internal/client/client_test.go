package client

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/hashicorp/yamux"
)

// fakeServer speaks the control protocol over the server side of a net.Pipe().
// It is the yamux server: it Accepts the client-opened control stream, expects
// an Auth, and either replies AuthOK (matching token) or an Error. handle, if
// non-nil, is invoked with the yamux session after successful auth so tests can
// drive data streams or additional control replies.
func fakeServer(t *testing.T, conn net.Conn, wantToken string, handle func(sess *yamux.Session, control net.Conn)) {
	t.Helper()
	sess, err := yamux.Server(conn, nil)
	if err != nil {
		t.Errorf("fakeServer: yamux.Server: %v", err)
		return
	}
	control, err := sess.Accept()
	if err != nil {
		t.Errorf("fakeServer: accept control: %v", err)
		return
	}
	msg, err := protocol.ReadMessage(control)
	if err != nil {
		t.Errorf("fakeServer: read auth: %v", err)
		return
	}
	auth, ok := msg.(protocol.Auth)
	if !ok {
		t.Errorf("fakeServer: expected Auth, got %T", msg)
		return
	}
	if auth.Token != wantToken {
		_ = protocol.WriteMessage(control, protocol.Error{Message: "bad token"})
		return
	}
	if err := protocol.WriteMessage(control, protocol.AuthOK{}); err != nil {
		t.Errorf("fakeServer: write authok: %v", err)
		return
	}
	if handle != nil {
		handle(sess, control)
	}
}

func TestConnectAuthenticates(t *testing.T) {
	cconn, sconn := net.Pipe()
	go fakeServer(t, sconn, "good-token", nil)
	c, err := connectOverConn(cconn, "good-token")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestConnectRejectsBadToken(t *testing.T) {
	cconn, sconn := net.Pipe()
	go fakeServer(t, sconn, "good-token", nil)
	c, err := connectOverConn(cconn, "wrong-token")
	if err == nil {
		c.Close()
		t.Fatal("expected error for bad token, got nil")
	}
}

func TestRegisterReturnsReply(t *testing.T) {
	cconn, sconn := net.Pipe()
	go fakeServer(t, sconn, "good-token", func(sess *yamux.Session, control net.Conn) {
		msg, err := protocol.ReadMessage(control)
		if err != nil {
			t.Errorf("read register: %v", err)
			return
		}
		if _, ok := msg.(protocol.Register); !ok {
			t.Errorf("expected Register, got %T", msg)
			return
		}
		_ = protocol.WriteMessage(control, protocol.Registered{
			PublicURL: "https://x.lvh.me",
			Host:      "x.lvh.me",
			Port:      443,
		})
	})
	c, err := connectOverConn(cconn, "good-token")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	reg, err := c.Register(TunnelRequest{Type: protocol.HTTP, LocalPort: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if reg.PublicURL != "https://x.lvh.me" {
		t.Fatalf("PublicURL = %q, want https://x.lvh.me", reg.PublicURL)
	}
}

func TestClaimAndListTunnels(t *testing.T) {
	cconn, sconn := net.Pipe()
	go fakeServer(t, sconn, "good-token", func(sess *yamux.Session, control net.Conn) {
		// Claim -> OK
		if _, err := protocol.ReadMessage(control); err != nil {
			t.Errorf("read claim: %v", err)
			return
		}
		if err := protocol.WriteMessage(control, protocol.OK{}); err != nil {
			t.Errorf("write ok: %v", err)
			return
		}
		// ListTunnels -> TunnelList
		if _, err := protocol.ReadMessage(control); err != nil {
			t.Errorf("read list: %v", err)
			return
		}
		_ = protocol.WriteMessage(control, protocol.TunnelList{
			Tunnels: []protocol.TunnelInfo{{Type: protocol.HTTP, Subdomain: "x", LocalPort: 3000}},
		})
	})
	c, err := connectOverConn(cconn, "good-token")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Claim("x"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	tunnels, err := c.ListTunnels()
	if err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	if len(tunnels) != 1 || tunnels[0].Subdomain != "x" {
		t.Fatalf("tunnels = %+v, want one with subdomain x", tunnels)
	}
}

func TestServeDialsLocalPortFromHeader(t *testing.T) {
	// Local echo listener the client is expected to dial.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c) // echo
			}(conn)
		}
	}()
	localPort := ln.Addr().(*net.TCPAddr).Port

	cconn, sconn := net.Pipe()
	done := make(chan struct{})
	go fakeServer(t, sconn, "good-token", func(sess *yamux.Session, control net.Conn) {
		defer close(done)
		// Server OPENS a data stream; client ACCEPTS it in Serve.
		stream, err := sess.Open()
		if err != nil {
			t.Errorf("server open data stream: %v", err)
			return
		}
		defer stream.Close()
		if err := protocol.WriteStreamHeader(stream, protocol.StreamHeader{LocalPort: localPort}); err != nil {
			t.Errorf("write stream header: %v", err)
			return
		}
		payload := []byte("hello-tunnel")
		if _, err := stream.Write(payload); err != nil {
			t.Errorf("write payload: %v", err)
			return
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(stream, buf); err != nil {
			t.Errorf("read echo: %v", err)
			return
		}
		if string(buf) != string(payload) {
			t.Errorf("echo = %q, want %q", buf, payload)
		}
	})

	c, err := connectOverConn(cconn, "good-token")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Serve(ctx)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for echo through tunnel")
	}
}
