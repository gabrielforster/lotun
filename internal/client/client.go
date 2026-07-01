// Package client implements the lotun client-side tunnel agent. It dials the
// control server, authenticates with a token, registers tunnels, and serves
// inbound proxy streams by forwarding them to a local port.
//
// The transport is a single yamux session over the control connection. The
// client is the yamux client (yamux.Client); the server is the yamux server.
// Two kinds of streams travel over the session:
//
//   - Control stream: the client OPENS it immediately after connecting. All
//     request/reply messages (Auth, Register, Claim, ...) travel on it.
//   - Data streams: the server OPENS one per inbound connection; the client
//     ACCEPTS them in Serve, reads a StreamHeader, and splices to localhost.
package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/hashicorp/yamux"
)

// Options configures how the client connects to the control server.
type Options struct {
	ControlAddr string
	Token       string
	UseTLS      bool // false for local/tests (plaintext)
	TLSInsecure bool // skip cert verify (self-signed control cert)
}

// TunnelRequest describes a tunnel the client asks the server to expose.
type TunnelRequest struct {
	Type       protocol.TunnelType
	Domain     string
	LocalPort  int
	RemotePort int
	Private    bool
	Password   string
	AllowIPs   []string
}

// Client is a connected, authenticated tunnel agent. It is safe for concurrent
// use: request/reply calls serialize on a mutex because they share the single
// control stream.
type Client struct {
	conn    net.Conn
	sess    *yamux.Session
	control net.Conn

	mu sync.Mutex // guards request/reply on the shared control stream
}

// Connect dials the control server, opens the control stream, and
// authenticates. It returns an error if the token is rejected.
func Connect(opts Options) (*Client, error) {
	var (
		conn net.Conn
		err  error
	)
	if opts.UseTLS {
		conn, err = tls.Dial("tcp", opts.ControlAddr, &tls.Config{
			InsecureSkipVerify: opts.TLSInsecure,
		})
	} else {
		conn, err = net.Dial("tcp", opts.ControlAddr)
	}
	if err != nil {
		return nil, err
	}
	return connectOverConn(conn, opts.Token)
}

// connectOverConn is the test seam Connect wraps: it takes an established
// net.Conn, sets up the yamux client session, opens the control stream, and
// performs the Auth/AuthOK handshake.
func connectOverConn(conn net.Conn, token string) (*Client, error) {
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Client OPENS the control stream immediately after connecting.
	control, err := sess.Open()
	if err != nil {
		sess.Close()
		conn.Close()
		return nil, err
	}
	if err := protocol.WriteMessage(control, protocol.Auth{Token: token}); err != nil {
		sess.Close()
		conn.Close()
		return nil, err
	}
	reply, err := protocol.ReadMessage(control)
	if err != nil {
		sess.Close()
		conn.Close()
		return nil, err
	}
	switch m := reply.(type) {
	case protocol.AuthOK:
		return &Client{conn: conn, sess: sess, control: control}, nil
	case protocol.Error:
		sess.Close()
		conn.Close()
		return nil, fmt.Errorf("auth rejected: %s", m.Message)
	default:
		sess.Close()
		conn.Close()
		return nil, fmt.Errorf("unexpected auth reply: %T", reply)
	}
}

// request sends one message on the control stream and returns the reply. It
// serializes access because the control stream is shared.
func (c *Client) request(m protocol.Message) (protocol.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := protocol.WriteMessage(c.control, m); err != nil {
		return nil, err
	}
	return protocol.ReadMessage(c.control)
}

// Register sends one registration and returns the server's reply.
func (c *Client) Register(req TunnelRequest) (protocol.Registered, error) {
	reply, err := c.request(protocol.Register{
		Type:       req.Type,
		Domain:     req.Domain,
		LocalPort:  req.LocalPort,
		RemotePort: req.RemotePort,
		Private:    req.Private,
		Password:   req.Password,
		AllowIPs:   req.AllowIPs,
	})
	if err != nil {
		return protocol.Registered{}, err
	}
	switch m := reply.(type) {
	case protocol.Registered:
		return m, nil
	case protocol.Error:
		return protocol.Registered{}, fmt.Errorf("register failed: %s", m.Message)
	default:
		return protocol.Registered{}, fmt.Errorf("unexpected register reply: %T", reply)
	}
}

// Claim requests ownership of a subdomain name. It is a one-shot control RPC.
func (c *Client) Claim(name string) error {
	return c.expectOK(protocol.Claim{Name: name})
}

// Unclaim releases ownership of a subdomain name. It is a one-shot control RPC.
func (c *Client) Unclaim(name string) error {
	return c.expectOK(protocol.Unclaim{Name: name})
}

// expectOK sends m and requires an OK reply, surfacing any Error message.
func (c *Client) expectOK(m protocol.Message) error {
	reply, err := c.request(m)
	if err != nil {
		return err
	}
	switch v := reply.(type) {
	case protocol.OK:
		return nil
	case protocol.Error:
		return fmt.Errorf("%s", v.Message)
	default:
		return fmt.Errorf("unexpected reply: %T", reply)
	}
}

// ListTunnels returns the active tunnels associated with the client's token.
func (c *Client) ListTunnels() ([]protocol.TunnelInfo, error) {
	reply, err := c.request(protocol.ListTunnels{})
	if err != nil {
		return nil, err
	}
	switch m := reply.(type) {
	case protocol.TunnelList:
		return m.Tunnels, nil
	case protocol.Error:
		return nil, fmt.Errorf("%s", m.Message)
	default:
		return nil, fmt.Errorf("unexpected list reply: %T", reply)
	}
}

// Serve blocks, accepting server-opened proxy streams. For each stream it reads
// the StreamHeader and pipes the stream to 127.0.0.1:<header.LocalPort>. It
// returns when ctx is cancelled or the control connection drops.
func (c *Client) Serve(ctx context.Context) error {
	// Unblock the Accept loop when the context is cancelled by closing the
	// session, which makes Accept return an error.
	go func() {
		<-ctx.Done()
		c.sess.Close()
	}()
	for {
		// Data streams are OPENED by the server; the client ACCEPTS them here.
		stream, err := c.sess.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go handleStream(stream)
	}
}

// handleStream reads the StreamHeader, dials the local port, and splices bytes
// in both directions until either side closes.
func handleStream(stream net.Conn) {
	defer stream.Close()
	header, err := protocol.ReadStreamHeader(stream)
	if err != nil {
		return
	}
	// ponytail: 127.0.0.1 only — the agent forwards to loopback by design.
	local, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(header.LocalPort))
	if err != nil {
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(local, stream)
		local.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(stream, local)
		stream.Close()
		done <- struct{}{}
	}()
	<-done
}

// Close tears down the control stream, session, and underlying connection.
func (c *Client) Close() error {
	if c.control != nil {
		c.control.Close()
	}
	if c.sess != nil {
		c.sess.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
