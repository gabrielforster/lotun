// Package server implements the lotun tunnel server: the in-memory tunnel
// registry (this file) plus the network-facing control, HTTP, and TCP handlers
// (other files). The registry is pure logic with no sockets, so it unit-tests
// without networking.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/gabrielrocha/lotun/internal/names"
	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/gabrielrocha/lotun/internal/store"
	"github.com/hashicorp/yamux"
)

// Registry errors returned by Register.
var (
	ErrNotClaimed       = errors.New("registry: subdomain not claimed")
	ErrSubdomainInUse   = errors.New("registry: subdomain already has an http tunnel")
	ErrPortInUse        = errors.New("registry: tcp port already in use")
	ErrPortOutOfRange   = errors.New("registry: requested port outside allowed range")
	ErrPortsExhausted   = errors.New("registry: no free tcp ports in range")
	ErrAllowIPsRequired = errors.New("registry: private tcp tunnel requires at least one --allow-ip")
)

// Tunnel is a single active tunnel routed by the registry.
type Tunnel struct {
	ID        string
	Type      protocol.TunnelType
	Subdomain string
	Port      int // tcp: public port; http: 0 (routed by subdomain, not port)
	LocalPort int
	Private   bool
	Password  string   // http basic-auth password (may be server-generated)
	AllowIPs  []string // tcp source-IP allowlist
	Session   *yamux.Session
}

// Registry tracks active tunnels and routes lookups by subdomain (http) or
// public port (tcp). It is safe for concurrent use.
type Registry struct {
	mu        sync.Mutex
	httpBySub map[string]*Tunnel
	tcpByPort map[int]*Tunnel
	claims    store.Store
	portMin   int
	portMax   int
}

// NewRegistry returns an empty Registry that allocates tcp ports in the
// inclusive range [portMin, portMax] and checks subdomain ownership against
// claims.
func NewRegistry(claims store.Store, portMin, portMax int) *Registry {
	return &Registry{
		httpBySub: make(map[string]*Tunnel),
		tcpByPort: make(map[int]*Tunnel),
		claims:    claims,
		portMin:   portMin,
		portMax:   portMax,
	}
}

// Register validates req, allocates a subdomain and/or tcp port as needed,
// records the tunnel, and returns it. See the package errors for the failure
// modes. password is stored verbatim for private http tunnels; Register does
// not invent one.
func (r *Registry) Register(req protocol.Register, sess *yamux.Session, password string) (*Tunnel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tn := &Tunnel{
		ID:        newID(),
		Type:      req.Type,
		LocalPort: req.LocalPort,
		Private:   req.Private,
		Password:  password,
		AllowIPs:  req.AllowIPs,
		Session:   sess,
	}

	// Resolve the subdomain: explicit domains must be claimed; empty domains
	// get a random unclaimed, uncollided name.
	sub := req.Domain
	if sub == "" {
		sub = r.freeName()
	} else if !r.claims.IsClaimed(sub) {
		return nil, ErrNotClaimed
	}
	tn.Subdomain = sub

	switch req.Type {
	case protocol.HTTP:
		if _, ok := r.httpBySub[sub]; ok {
			return nil, ErrSubdomainInUse
		}
		r.httpBySub[sub] = tn
	case protocol.TCP:
		if req.Private && len(req.AllowIPs) == 0 {
			return nil, ErrAllowIPsRequired
		}
		port, err := r.allocPort(req.RemotePort, req.LocalPort)
		if err != nil {
			return nil, err
		}
		tn.Port = port
		r.tcpByPort[port] = tn
	}

	return tn, nil
}

// freeName returns a random generated name that is neither claimed nor already
// serving an http tunnel. The caller must hold r.mu.
func (r *Registry) freeName() string {
	for i := 0; i < 1000; i++ {
		n := names.Generate()
		if _, inUse := r.httpBySub[n]; !inUse && !r.claims.IsClaimed(n) {
			return n
		}
	}
	// 900 combos exhausted 1000 times is effectively impossible single-tenant;
	// fall back to the last generated name rather than looping forever.
	return names.Generate()
}

// allocPort resolves the public tcp port. A non-zero want must be in range and
// free. When want==0 the port defaults to localPort if that is in range and
// free; otherwise it scans for the lowest free port in range. The caller must
// hold r.mu.
//
// ponytail: linear port scan over the range — range is ~10k, trivial. Bitset only if it ever shows up in a profile.
func (r *Registry) allocPort(want, localPort int) (int, error) {
	if want != 0 {
		if want < r.portMin || want > r.portMax {
			return 0, ErrPortOutOfRange
		}
		if _, inUse := r.tcpByPort[want]; inUse {
			return 0, ErrPortInUse
		}
		return want, nil
	}
	// RemotePort omitted: prefer the same port number as LocalPort when it is
	// available, else fall back to the lowest free port in range.
	if localPort >= r.portMin && localPort <= r.portMax {
		if _, inUse := r.tcpByPort[localPort]; !inUse {
			return localPort, nil
		}
	}
	for p := r.portMin; p <= r.portMax; p++ {
		if _, inUse := r.tcpByPort[p]; !inUse {
			return p, nil
		}
	}
	return 0, ErrPortsExhausted
}

// LookupHTTP returns the http tunnel for subdomain, if any.
func (r *Registry) LookupHTTP(subdomain string) (*Tunnel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tn, ok := r.httpBySub[subdomain]
	return tn, ok
}

// LookupTCP returns the tcp tunnel bound to port, if any.
func (r *Registry) LookupTCP(port int) (*Tunnel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tn, ok := r.tcpByPort[port]
	return tn, ok
}

// Remove deletes the tunnel with the given ID from all routing tables.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sub, tn := range r.httpBySub {
		if tn.ID == id {
			delete(r.httpBySub, sub)
		}
	}
	for port, tn := range r.tcpByPort {
		if tn.ID == id {
			delete(r.tcpByPort, port)
		}
	}
}

// RemoveSession removes every tunnel belonging to sess and returns the freed
// tcp ports so the caller can close their listeners.
func (r *Registry) RemoveSession(sess *yamux.Session) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sub, tn := range r.httpBySub {
		if tn.Session == sess {
			delete(r.httpBySub, sub)
		}
	}
	freed := []int{}
	for port, tn := range r.tcpByPort {
		if tn.Session == sess {
			freed = append(freed, port)
			delete(r.tcpByPort, port)
		}
	}
	return freed
}

// List returns all active tunnels in no particular order.
func (r *Registry) List() []*Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Tunnel, 0, len(r.httpBySub)+len(r.tcpByPort))
	for _, tn := range r.httpBySub {
		out = append(out, tn)
	}
	for _, tn := range r.tcpByPort {
		out = append(out, tn)
	}
	return out
}

// newID returns a short random hex identifier used in data-stream headers.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("server: unable to read crypto/rand for tunnel ID: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
