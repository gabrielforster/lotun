package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/gabrielrocha/lotun/internal/protocol"
)

// httpProxy is the public HTTP handler. It resolves the request Host to a
// tunnel subdomain, enforces Basic Auth for private tunnels, and reverse-
// proxies the request over a yamux data stream to the client's local port.
type httpProxy struct {
	registry *Registry
	base     string

	// proxies caches one *httputil.ReverseProxy per tunnel ID; each carries a
	// Transport whose DialContext opens a fresh yamux stream. Guarded by mu.
	mu      sync.Mutex
	proxies map[string]*httputil.ReverseProxy
}

// newHTTPProxy builds an httpProxy routing subdomains under base.
func newHTTPProxy(registry *Registry, base string) *httpProxy {
	return &httpProxy{
		registry: registry,
		base:     base,
		proxies:  make(map[string]*httputil.ReverseProxy),
	}
}

// ServeHTTP routes one public request: 404 when the subdomain has no tunnel,
// 401 when a private tunnel's Basic Auth fails, otherwise reverse-proxy.
func (p *httpProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub, ok := subdomainFromHost(r.Host, p.base)
	if !ok {
		http.NotFound(w, r)
		return
	}
	tn, ok := p.registry.LookupHTTP(sub)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if tn.Private && !basicAuthOK(r, tn.Password) {
		w.Header().Set("WWW-Authenticate", `Basic realm="lotun"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p.proxyFor(tn).ServeHTTP(w, r)
}

// proxyFor returns the cached reverse proxy for tn, creating it on first use.
func (p *httpProxy) proxyFor(tn *Tunnel) *httputil.ReverseProxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rp, ok := p.proxies[tn.ID]; ok {
		return rp
	}

	// The dial target host is a placeholder: our DialContext ignores it and
	// opens a yamux stream instead. Scheme http keeps the proxy from wrapping
	// the transport in TLS.
	target := &url.URL{Scheme: "http", Host: "tunnel.invalid"}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return openDataStream(tn)
		},
	}
	p.proxies[tn.ID] = rp
	return rp
}

// openDataStream opens a new yamux data stream on tn's session and writes the
// StreamHeader that tells the client which local port to dial. This server is
// the yamux SERVER and OPENS data streams; the client ACCEPTS them.
func openDataStream(tn *Tunnel) (net.Conn, error) {
	stream, err := tn.Session.Open()
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteStreamHeader(stream, protocol.StreamHeader{
		TunnelID:  tn.ID,
		LocalPort: tn.LocalPort,
	}); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}
