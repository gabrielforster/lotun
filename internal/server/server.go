package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gabrielrocha/lotun/internal/config"
	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/gabrielrocha/lotun/internal/store"
	"github.com/hashicorp/yamux"
)

// Server is the lotun data plane: it accepts control connections (yamux),
// authenticates and services registration RPCs, routes public HTTP requests to
// the right tunnel by subdomain, and opens a public TCP listener per tcp
// tunnel. Route New a config and a claims store, then call Run.
//
// yamux roles: the client is the yamux client and OPENS the control stream;
// this server is the yamux server and ACCEPTS it. Data streams are OPENED by
// this server (see http.go and tcp.go) and ACCEPTED by the client.
type Server struct {
	cfg      config.ServerConfig
	claims   store.Store
	registry *Registry

	httpProxy *httpProxy

	// bound addresses, populated once the listeners bind (useful with :0).
	mu       sync.Mutex
	ctrlAddr string
	httpAddr string

	// tcpListeners maps public tcp port -> its listener so session cleanup can
	// close them. Guarded by tcpMu.
	tcpMu        sync.Mutex
	tcpListeners map[int]net.Listener
}

// New validates cfg and returns a Server ready to Run. It does not bind any
// sockets; binding happens in Run so tests can use :0 and read the resolved
// address afterwards.
func New(cfg config.ServerConfig, claims store.Store) (*Server, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("server: empty token")
	}
	if cfg.BaseDomain == "" {
		return nil, fmt.Errorf("server: empty base domain")
	}
	s := &Server{
		cfg:          cfg,
		claims:       claims,
		registry:     NewRegistry(claims, cfg.TCPPortMin, cfg.TCPPortMax),
		tcpListeners: make(map[int]net.Listener),
	}
	s.httpProxy = newHTTPProxy(s.registry, cfg.BaseDomain)
	return s, nil
}

// Run starts the control and HTTP listeners (and TCP listeners on demand) and
// blocks until ctx is cancelled, then drains. It returns the first fatal error,
// or nil on clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	ctrlLn, err := s.listenControl()
	if err != nil {
		return fmt.Errorf("server: control listen: %w", err)
	}
	s.setControlAddr(ctrlLn.Addr().String())

	httpLn, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		ctrlLn.Close()
		return fmt.Errorf("server: http listen: %w", err)
	}
	s.setHTTPAddr(httpLn.Addr().String())

	httpSrv := &http.Server{Handler: s.httpProxy}

	// Cancellation: close both listeners and the HTTP server to unblock the
	// accept loop and Serve.
	go func() {
		<-ctx.Done()
		ctrlLn.Close()
		httpSrv.Close()
		s.closeAllTCP()
	}()

	errc := make(chan error, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			select {
			case errc <- err:
			default:
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.acceptControl(ctx, ctrlLn)
	}()

	wg.Wait()

	select {
	case err := <-errc:
		return err
	default:
		return nil
	}
}

// ControlAddr returns the bound control listener address, or "" before Run has
// bound it. Useful when configured with :0.
func (s *Server) ControlAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctrlAddr
}

// HTTPAddr returns the bound HTTP listener address, or "" before Run has bound
// it. Useful when configured with :0.
func (s *Server) HTTPAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.httpAddr
}

func (s *Server) setControlAddr(a string) { s.mu.Lock(); s.ctrlAddr = a; s.mu.Unlock() }
func (s *Server) setHTTPAddr(a string)    { s.mu.Lock(); s.httpAddr = a; s.mu.Unlock() }

// listenControl binds the control listener, wrapping it in TLS when a cert is
// configured (plaintext otherwise, for dev/test).
func (s *Server) listenControl() (net.Listener, error) {
	if s.cfg.ControlTLSCert != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.ControlTLSCert, s.cfg.ControlTLSKey)
		if err != nil {
			return nil, err
		}
		cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
		return tls.Listen("tcp", s.cfg.ControlAddr, cfg)
	}
	return net.Listen("tcp", s.cfg.ControlAddr)
}

// acceptControl accepts control connections until ctx is cancelled (which
// closes ctrlLn and makes Accept fail). Each connection is handled in its own
// goroutine.
func (s *Server) acceptControl(ctx context.Context, ctrlLn net.Listener) {
	for {
		conn, err := ctrlLn.Accept()
		if err != nil {
			return // listener closed (shutdown) or fatal accept error
		}
		go s.handleControlConn(conn)
	}
}

// handleControlConn runs a single control connection: it becomes the yamux
// server, accepts the client-opened control stream, authenticates, and then
// services registration RPCs until the connection drops. On close it removes
// every tunnel owned by the session and closes their tcp listeners.
func (s *Server) handleControlConn(conn net.Conn) {
	defer conn.Close()

	// This server is the yamux SERVER; the client is the yamux client.
	sess, err := yamux.Server(conn, nil)
	if err != nil {
		return
	}
	defer sess.Close()

	// The client OPENS the control stream; we ACCEPT it.
	control, err := sess.Accept()
	if err != nil {
		return
	}

	if !s.authenticate(control) {
		return
	}

	// Ensure the session's tunnels and listeners are cleaned up on exit.
	defer func() {
		ports := s.registry.RemoveSession(sess)
		for _, p := range ports {
			s.closeTCP(p)
		}
	}()

	s.serveControl(control, sess)
}

// authenticate reads the first control message, which must be Auth, and
// compares the token in constant time. It replies AuthOK on success or Error on
// failure, and reports whether the client may proceed.
func (s *Server) authenticate(control net.Conn) bool {
	msg, err := protocol.ReadMessage(control)
	if err != nil {
		return false
	}
	auth, ok := msg.(protocol.Auth)
	if !ok {
		protocol.WriteMessage(control, protocol.Error{Message: "expected auth"})
		return false
	}
	if subtle.ConstantTimeCompare([]byte(auth.Token), []byte(s.cfg.Token)) != 1 {
		protocol.WriteMessage(control, protocol.Error{Message: "invalid token"})
		return false
	}
	return protocol.WriteMessage(control, protocol.AuthOK{}) == nil
}

// serveControl loops over control messages, dispatching Register/Claim/Unclaim/
// ListTunnels and replying on the same stream. It returns when the stream
// closes or an unrecoverable read/write error occurs.
func (s *Server) serveControl(control net.Conn, sess *yamux.Session) {
	for {
		msg, err := protocol.ReadMessage(control)
		if err != nil {
			return
		}
		var reply protocol.Message
		switch m := msg.(type) {
		case protocol.Register:
			reply = s.handleRegister(m, sess)
		case protocol.Claim:
			reply = s.handleClaim(m.Name)
		case protocol.Unclaim:
			reply = s.handleUnclaim(m.Name)
		case protocol.ListTunnels:
			reply = s.handleList()
		default:
			reply = protocol.Error{Message: fmt.Sprintf("unexpected message %T", msg)}
		}
		if err := protocol.WriteMessage(control, reply); err != nil {
			return
		}
	}
}

// handleRegister validates and records a registration. For private http
// tunnels with no supplied password it generates one; for tcp tunnels it also
// starts the public listener. It returns Registered on success or Error.
func (s *Server) handleRegister(req protocol.Register, sess *yamux.Session) protocol.Message {
	password := req.Password
	generated := ""
	if req.Type == protocol.HTTP && req.Private && password == "" {
		password = generatePassword()
		generated = password
	}

	tn, err := s.registry.Register(req, sess, password)
	if err != nil {
		return protocol.Error{Message: err.Error()}
	}

	switch tn.Type {
	case protocol.HTTP:
		return protocol.Registered{
			PublicURL:         fmt.Sprintf("https://%s.%s", tn.Subdomain, s.cfg.BaseDomain),
			Host:              fmt.Sprintf("%s.%s", tn.Subdomain, s.cfg.BaseDomain),
			Port:              443,
			GeneratedPassword: generated,
		}
	case protocol.TCP:
		if err := s.startTCPTunnel(tn); err != nil {
			s.registry.Remove(tn.ID)
			return protocol.Error{Message: err.Error()}
		}
		host := fmt.Sprintf("%s.%s", tn.Subdomain, s.cfg.BaseDomain)
		return protocol.Registered{
			PublicURL: fmt.Sprintf("tcp://%s:%d", host, tn.Port),
			Host:      host,
			Port:      tn.Port,
		}
	default:
		s.registry.Remove(tn.ID)
		return protocol.Error{Message: "unknown tunnel type"}
	}
}

// handleClaim claims a subdomain for the token's owner.
func (s *Server) handleClaim(name string) protocol.Message {
	if err := s.claims.Claim(name); err != nil {
		return protocol.Error{Message: err.Error()}
	}
	return protocol.OK{}
}

// handleUnclaim releases a subdomain claim.
func (s *Server) handleUnclaim(name string) protocol.Message {
	if err := s.claims.Release(name); err != nil {
		return protocol.Error{Message: err.Error()}
	}
	return protocol.OK{}
}

// handleList builds a TunnelList from the current registry contents.
func (s *Server) handleList() protocol.Message {
	tunnels := s.registry.List()
	out := make([]protocol.TunnelInfo, 0, len(tunnels))
	for _, tn := range tunnels {
		info := protocol.TunnelInfo{
			Type:      tn.Type,
			Subdomain: tn.Subdomain,
			Port:      tn.Port,
			LocalPort: tn.LocalPort,
		}
		host := fmt.Sprintf("%s.%s", tn.Subdomain, s.cfg.BaseDomain)
		if tn.Type == protocol.HTTP {
			info.PublicURL = "https://" + host
		} else {
			info.PublicURL = fmt.Sprintf("tcp://%s:%d", host, tn.Port)
		}
		out = append(out, info)
	}
	return protocol.TunnelList{Tunnels: out}
}

// closeTCP closes and forgets the listener for a single public port.
func (s *Server) closeTCP(port int) {
	s.tcpMu.Lock()
	ln, ok := s.tcpListeners[port]
	if ok {
		delete(s.tcpListeners, port)
	}
	s.tcpMu.Unlock()
	if ok {
		ln.Close()
	}
}

// closeAllTCP closes every tracked tcp listener (shutdown path).
func (s *Server) closeAllTCP() {
	s.tcpMu.Lock()
	lns := s.tcpListeners
	s.tcpListeners = make(map[int]net.Listener)
	s.tcpMu.Unlock()
	for _, ln := range lns {
		ln.Close()
	}
}

// subdomainFromHost extracts the leftmost label of host relative to base,
// stripping any port. It reports false when host is exactly base (no
// subdomain) or is not a subdomain of base at all.
//
//	subdomainFromHost("myapp.lvh.me:8000", "lvh.me") => ("myapp", true)
//	subdomainFromHost("lvh.me", "lvh.me")            => ("", false)
func subdomainFromHost(host, base string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	sub := strings.TrimSuffix(host, suffix)
	if sub == "" {
		return "", false
	}
	return sub, true
}

// basicAuthOK reports whether r carries HTTP Basic credentials for user
// "lotun" with the given password, compared in constant time.
func basicAuthOK(r *http.Request, password string) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte("lotun")) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1
	return userOK && passOK
}

// ipAllowed reports whether the IP portion of remoteAddr is in allow. An empty
// allow list means allow-all (a public tunnel).
func ipAllowed(remoteAddr string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	ip := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = h
	}
	for _, a := range allow {
		if a == ip {
			return true
		}
	}
	return false
}

// passwordAlphabet is the base62 set used for generated http passwords.
const passwordAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// generatePassword returns a 16-character base62 password from crypto/rand.
func generatePassword() string {
	const n = 16
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("server: unable to read crypto/rand for password: " + err.Error())
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = passwordAlphabet[int(v)%len(passwordAlphabet)]
	}
	return string(out)
}
