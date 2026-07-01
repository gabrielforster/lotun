// Package e2e holds in-process end-to-end tests that wire a real lotun server
// and a real lotun client together over loopback and prove traffic round-trips
// for HTTP and TCP tunnels, both public and private.
package e2e

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gabrielrocha/lotun/internal/client"
	"github.com/gabrielrocha/lotun/internal/config"
	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/gabrielrocha/lotun/internal/server"
	"github.com/gabrielrocha/lotun/internal/store"
)

// harness is a running server plus a connected client, all bound to loopback
// with plaintext control. Everything is torn down via t.Cleanup.
type harness struct {
	srv        *server.Server
	cl         *client.Client
	claims     store.Store
	httpAddr   string
	baseDomain string
}

// newHarness starts a plaintext server on random loopback ports, waits for it
// to bind, and connects a plaintext client. cancel/Close are registered with
// t.Cleanup so callers only need to call newHarness.
func newHarness(t *testing.T) *harness {
	t.Helper()

	const token = "e2e-test-token"
	const baseDomain = "lvh.me"

	claims, err := store.Open(filepath.Join(t.TempDir(), "claims.json"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	cfg := config.ServerConfig{
		ControlAddr: "127.0.0.1:0",
		HTTPAddr:    "127.0.0.1:0",
		BaseDomain:  baseDomain,
		TCPPortMin:  20000,
		TCPPortMax:  30000,
		Token:       token,
		DataDir:     t.TempDir(),
	}

	srv, err := server.New(cfg, claims)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = srv.Run(ctx) }()

	// Wait for the listeners to bind so the addresses resolve.
	deadline := time.Now().Add(3 * time.Second)
	for srv.ControlAddr() == "" || srv.HTTPAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server listeners did not bind within deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cl, err := client.Connect(client.Options{
		ControlAddr: srv.ControlAddr(),
		Token:       token,
		UseTLS:      false,
	})
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	return &harness{
		srv:        srv,
		cl:         cl,
		claims:     claims,
		httpAddr:   srv.HTTPAddr(),
		baseDomain: baseDomain,
	}
}

// serve starts cl.Serve in a goroutine bound to a context cancelled on cleanup.
func serve(t *testing.T, cl *client.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = cl.Serve(ctx) }()
}

// portOf extracts the numeric port from a host:port address.
func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return port
}

// TestHTTPTunnelRoundTrip registers a public HTTP tunnel with a random domain
// and proves a GET against the server's public HTTP addr is proxied to the
// local upstream.
func TestHTTPTunnelRoundTrip(t *testing.T) {
	h := newHarness(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from local")
	}))
	t.Cleanup(upstream.Close)

	reg, err := h.cl.Register(client.TunnelRequest{
		Type:      protocol.HTTP,
		LocalPort: portOf(t, upstream.Listener.Addr().String()),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.Host == "" {
		t.Fatalf("expected a non-empty Host, got %q", reg.Host)
	}

	serve(t, h.cl)

	body := httpGet(t, h.httpAddr, reg.Host, "")
	if body != "hello from local" {
		t.Fatalf("body = %q, want %q", body, "hello from local")
	}
}

// TestHTTPPrivateTunnelBasicAuth registers a private HTTP tunnel with no
// password (server generates one) and proves Basic Auth is enforced.
func TestHTTPPrivateTunnelBasicAuth(t *testing.T) {
	h := newHarness(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secret area")
	}))
	t.Cleanup(upstream.Close)

	const domain = "private-app"
	if err := h.cl.Claim(domain); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	reg, err := h.cl.Register(client.TunnelRequest{
		Type:      protocol.HTTP,
		Domain:    domain,
		Private:   true,
		LocalPort: portOf(t, upstream.Listener.Addr().String()),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.GeneratedPassword == "" {
		t.Fatal("expected a non-empty GeneratedPassword")
	}

	serve(t, h.cl)

	// No credentials -> 401.
	if code := httpStatus(t, h.httpAddr, reg.Host, ""); code != http.StatusUnauthorized {
		t.Fatalf("without creds: status = %d, want 401", code)
	}

	// Correct lotun:<generatedPassword> -> 200 and body.
	creds := "lotun:" + reg.GeneratedPassword
	if code := httpStatus(t, h.httpAddr, reg.Host, creds); code != http.StatusOK {
		t.Fatalf("with creds: status = %d, want 200", code)
	}
	if body := httpGet(t, h.httpAddr, reg.Host, creds); body != "secret area" {
		t.Fatalf("with creds: body = %q, want %q", body, "secret area")
	}
}

// TestTCPTunnelRoundTrip registers a TCP tunnel on a claimed domain and proves
// bytes dialed at the public port are echoed by the local listener.
func TestTCPTunnelRoundTrip(t *testing.T) {
	h := newHarness(t)

	echoLn := newEchoListener(t)

	const domain = "tcp-app"
	if err := h.cl.Claim(domain); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	reg, err := h.cl.Register(client.TunnelRequest{
		Type:      protocol.TCP,
		Domain:    domain,
		LocalPort: portOf(t, echoLn.Addr().String()),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.Port < 20000 || reg.Port > 30000 {
		t.Fatalf("reg.Port = %d, want in [20000,30000]", reg.Port)
	}

	serve(t, h.cl)

	got := tcpExchange(t, reg.Port, "ping")
	if got != "ping" {
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
}

// TestTCPPrivateAllowlist proves a private TCP tunnel that allowlists 127.0.0.1
// serves loopback connections, while one that allowlists a foreign IP drops the
// loopback connection (the read sees EOF).
func TestTCPPrivateAllowlist(t *testing.T) {
	h := newHarness(t)

	echoLn := newEchoListener(t)
	localPort := portOf(t, echoLn.Addr().String())

	// Allowed: 127.0.0.1 is in the allowlist, so the exchange succeeds.
	if err := h.cl.Claim("allowed-app"); err != nil {
		t.Fatalf("Claim(allowed-app): %v", err)
	}
	allowed, err := h.cl.Register(client.TunnelRequest{
		Type:      protocol.TCP,
		Domain:    "allowed-app",
		LocalPort: localPort,
		Private:   true,
		AllowIPs:  []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("Register(allowed): %v", err)
	}

	// Denied: the allowlist holds only a foreign IP, so a loopback connection is
	// dropped by the server after Accept.
	if err := h.cl.Claim("denied-app"); err != nil {
		t.Fatalf("Claim(denied-app): %v", err)
	}
	denied, err := h.cl.Register(client.TunnelRequest{
		Type:      protocol.TCP,
		Domain:    "denied-app",
		LocalPort: localPort,
		Private:   true,
		AllowIPs:  []string{"203.0.113.1"},
	})
	if err != nil {
		t.Fatalf("Register(denied): %v", err)
	}

	serve(t, h.cl)

	if got := tcpExchange(t, allowed.Port, "ping"); got != "ping" {
		t.Fatalf("allowlisted echo = %q, want %q", got, "ping")
	}

	// The denied connection is accepted at the TCP layer then immediately closed
	// server-side; the write may or may not succeed, but the read must see EOF
	// (no echo) within the deadline.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(denied.Port)), 5*time.Second)
	if err != nil {
		// A refused/reset dial is also an acceptable "dropped" outcome.
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("ping"))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		t.Fatalf("denied tunnel echoed %q; expected the connection to be dropped", string(buf[:n]))
	}
}

// newEchoListener starts a loopback TCP listener that echoes back everything it
// reads, on its own goroutine, torn down on cleanup.
func newEchoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln
}

// tcpExchange dials the public port, writes msg, and returns what it reads back
// (bounded by a deadline).
func tcpExchange(t *testing.T, port int, msg string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial tcp :%d: %v", port, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf)
}

// httpClient returns a client with a bounded timeout so hangs fail fast.
func httpClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

// newHTTPRequest builds a GET against the server's public HTTP addr with the
// given tunnel Host header and optional "user:pass" basic-auth creds.
func newHTTPRequest(t *testing.T, httpAddr, host, creds string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", "http://"+httpAddr+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = host
	if creds != "" {
		user, pass, _ := splitCreds(creds)
		req.SetBasicAuth(user, pass)
	}
	return req
}

// httpGet performs the request and returns the response body as a string.
func httpGet(t *testing.T, httpAddr, host, creds string) string {
	t.Helper()
	resp, err := httpClient().Do(newHTTPRequest(t, httpAddr, host, creds))
	if err != nil {
		t.Fatalf("GET %s: %v", host, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// httpStatus performs the request and returns the status code.
func httpStatus(t *testing.T, httpAddr, host, creds string) int {
	t.Helper()
	resp, err := httpClient().Do(newHTTPRequest(t, httpAddr, host, creds))
	if err != nil {
		t.Fatalf("GET %s: %v", host, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// splitCreds splits "user:pass" into its parts (pass may itself contain ':').
func splitCreds(creds string) (user, pass string, ok bool) {
	for i := 0; i < len(creds); i++ {
		if creds[i] == ':' {
			return creds[:i], creds[i+1:], true
		}
	}
	return creds, "", false
}
