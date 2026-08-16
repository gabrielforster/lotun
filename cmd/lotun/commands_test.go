package main

import (
	"testing"

	"github.com/gabrielrocha/lotun/internal/config"
	"github.com/gabrielrocha/lotun/internal/protocol"
)

func TestTCPPrivateRequiresAllowIP(t *testing.T) {
	err := validateTCPFlags( /*private=*/ true /*allowIPs=*/, nil /*password=*/, "")
	if err == nil {
		t.Fatal("private tcp with no --allow-ip must error")
	}
}

func TestTCPPasswordRejected(t *testing.T) {
	if validateTCPFlags(true, []string{"1.2.3.4"}, "pw") == nil {
		t.Fatal("--password on tcp must error")
	}
}

func TestTCPPrivateWithAllowIPOK(t *testing.T) {
	if err := validateTCPFlags(true, []string{"1.2.3.4"}, ""); err != nil {
		t.Fatalf("private tcp with --allow-ip must succeed, got %v", err)
	}
}

func TestTCPPublicOK(t *testing.T) {
	if err := validateTCPFlags(false, nil, ""); err != nil {
		t.Fatalf("public tcp must succeed, got %v", err)
	}
}

func TestTunnelRequestsConvertsEveryEntry(t *testing.T) {
	got, err := tunnelRequests([]config.TunnelConfig{
		{Type: "http", Domain: "api", Port: 3000},
		{Type: "TCP", Domain: "db", Port: 5432, RemotePort: 25432, Private: true, AllowIPs: []string{"1.2.3.4"}},
	})
	if err != nil {
		t.Fatalf("tunnelRequests: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Type != protocol.HTTP || got[0].Domain != "api" || got[0].LocalPort != 3000 {
		t.Fatalf("http entry mismatch: %#v", got[0])
	}
	// The type is case-insensitive and the tcp-only fields carry through.
	if got[1].Type != protocol.TCP || got[1].RemotePort != 25432 || len(got[1].AllowIPs) != 1 {
		t.Fatalf("tcp entry mismatch: %#v", got[1])
	}
}

func TestTunnelRequestsRejectsBadEntries(t *testing.T) {
	cases := map[string][]config.TunnelConfig{
		"empty list":                    nil,
		"unknown type":                  {{Type: "udp", Port: 80}},
		"missing port":                  {{Type: "http"}},
		"port out of range":             {{Type: "http", Port: 70000}},
		"private tcp without allow_ips": {{Type: "tcp", Port: 5432, Private: true}},
		"password on tcp":               {{Type: "tcp", Port: 5432, Password: "pw"}},
	}
	for name, tunnels := range cases {
		if _, err := tunnelRequests(tunnels); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestHTTPPortParsing(t *testing.T) {
	if p, err := parsePort("8080"); err != nil || p != 8080 {
		t.Fatalf(`parsePort("8080") = (%d, %v), want (8080, nil)`, p, err)
	}
	for _, bad := range []string{"0", "70000", "abc", "", "-1"} {
		if _, err := parsePort(bad); err == nil {
			t.Fatalf("parsePort(%q) must error", bad)
		}
	}
}
