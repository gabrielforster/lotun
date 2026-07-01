package server

import (
	"path/filepath"
	"testing"

	"github.com/gabrielrocha/lotun/internal/protocol"
	"github.com/gabrielrocha/lotun/internal/store"
)

// memStore returns an empty, disk-backed store rooted in the test's temp dir.
func memStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "claims.json"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}

// claimed returns a store with name pre-claimed.
func claimed(t *testing.T, name string) store.Store {
	t.Helper()
	s := memStore(t)
	if err := s.Claim(name); err != nil {
		t.Fatalf("Claim(%q): %v", name, err)
	}
	return s
}

func TestRegisterHTTPRandomName(t *testing.T) {
	r := NewRegistry(memStore(t), 20000, 20010)
	tn, err := r.Register(protocol.Register{Type: protocol.HTTP, LocalPort: 8080}, nil, "")
	if err != nil || tn.Subdomain == "" {
		t.Fatalf("want name, got %v %v", tn, err)
	}
	if _, ok := r.LookupHTTP(tn.Subdomain); !ok {
		t.Fatal("not routable")
	}
	if tn.ID == "" {
		t.Fatal("want non-empty tunnel ID")
	}
	if tn.LocalPort != 8080 {
		t.Fatalf("want LocalPort 8080, got %d", tn.LocalPort)
	}
}

func TestRegisterHTTPDuplicateSubdomain(t *testing.T) {
	r := NewRegistry(claimed(t, "myapp"), 20000, 20010)
	if _, err := r.Register(protocol.Register{Type: protocol.HTTP, Domain: "myapp", LocalPort: 80}, nil, ""); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := r.Register(protocol.Register{Type: protocol.HTTP, Domain: "myapp", LocalPort: 81}, nil, "")
	if err != ErrSubdomainInUse {
		t.Fatalf("want ErrSubdomainInUse, got %v", err)
	}
}

func TestRegisterUnclaimedDomainRejected(t *testing.T) {
	r := NewRegistry(memStore(t), 20000, 20010)
	_, err := r.Register(protocol.Register{Type: protocol.HTTP, Domain: "taken", LocalPort: 80}, nil, "")
	if err != ErrNotClaimed {
		t.Fatalf("want ErrNotClaimed, got %v", err)
	}
}

func TestRegisterTCPDefaultsRemoteToLocal(t *testing.T) {
	r := NewRegistry(claimed(t, "game"), 20000, 30000)
	tn, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "game", LocalPort: 25565, AllowIPs: nil}, nil, "")
	if err != nil || tn.Port != 25565 {
		t.Fatalf("want port 25565, got %v %v", tn, err)
	}
	if _, ok := r.LookupTCP(25565); !ok {
		t.Fatal("tcp not routable")
	}
}

func TestRegisterTCPAllocatesLowestFree(t *testing.T) {
	r := NewRegistry(claimed(t, "svc"), 20000, 20010)
	tn, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 9000, RemotePort: 0}, nil, "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if tn.Port != 20000 {
		t.Fatalf("want lowest free port 20000, got %d", tn.Port)
	}
}

func TestRegisterTCPPortInUseAndOutOfRange(t *testing.T) {
	r := NewRegistry(claimed(t, "svc"), 20000, 30000)
	if _, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000, RemotePort: 25000}, nil, ""); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000, RemotePort: 25000}, nil, "")
	if err != ErrPortInUse {
		t.Fatalf("want ErrPortInUse, got %v", err)
	}
	_, err = r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000, RemotePort: 99999}, nil, "")
	if err != ErrPortOutOfRange {
		t.Fatalf("want ErrPortOutOfRange, got %v", err)
	}
}

func TestRegisterTCPPortsExhausted(t *testing.T) {
	r := NewRegistry(claimed(t, "svc"), 20000, 20001)
	for i := 0; i < 2; i++ {
		if _, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000}, nil, ""); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	_, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000}, nil, "")
	if err != ErrPortsExhausted {
		t.Fatalf("want ErrPortsExhausted, got %v", err)
	}
}

func TestRegisterPrivateTCPWithoutAllowIPs(t *testing.T) {
	r := NewRegistry(claimed(t, "db"), 20000, 30000)
	_, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "db", LocalPort: 5432, Private: true}, nil, "")
	if err != ErrAllowIPsRequired {
		t.Fatalf("want ErrAllowIPsRequired, got %v", err)
	}
}

func TestRegisterPrivateTCPWithAllowIPs(t *testing.T) {
	r := NewRegistry(claimed(t, "db"), 20000, 30000)
	tn, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "db", LocalPort: 5432, Private: true, AllowIPs: []string{"10.0.0.1"}}, nil, "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !tn.Private || len(tn.AllowIPs) != 1 {
		t.Fatalf("want private tunnel with allow ips, got %+v", tn)
	}
}

func TestRegisterHTTPPrivatePasswordStored(t *testing.T) {
	r := NewRegistry(claimed(t, "secret"), 20000, 30000)
	tn, err := r.Register(protocol.Register{Type: protocol.HTTP, Domain: "secret", LocalPort: 80, Private: true}, nil, "hunter2")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if tn.Password != "hunter2" {
		t.Fatalf("want password stored, got %q", tn.Password)
	}
}

func TestRemoveByID(t *testing.T) {
	r := NewRegistry(claimed(t, "app"), 20000, 30000)
	tn, err := r.Register(protocol.Register{Type: protocol.HTTP, Domain: "app", LocalPort: 80}, nil, "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	r.Remove(tn.ID)
	if _, ok := r.LookupHTTP("app"); ok {
		t.Fatal("tunnel should be gone after Remove")
	}
}

func TestRemoveSessionFreesPorts(t *testing.T) {
	r := NewRegistry(claimed(t, "svc"), 20000, 30000)
	tn1, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000, RemotePort: 21000}, nil, "")
	if err != nil {
		t.Fatalf("register 1: %v", err)
	}
	tn2, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1001, RemotePort: 21001}, nil, "")
	if err != nil {
		t.Fatalf("register 2: %v", err)
	}

	freed := r.RemoveSession(nil)
	if len(freed) != 2 {
		t.Fatalf("want 2 freed ports, got %v", freed)
	}
	gotPorts := map[int]bool{freed[0]: true, freed[1]: true}
	if !gotPorts[tn1.Port] || !gotPorts[tn2.Port] {
		t.Fatalf("freed ports %v do not match %d and %d", freed, tn1.Port, tn2.Port)
	}
	if _, ok := r.LookupTCP(tn1.Port); ok {
		t.Fatal("port 1 still routable")
	}
	if _, ok := r.LookupTCP(tn2.Port); ok {
		t.Fatal("port 2 still routable")
	}
}

func TestList(t *testing.T) {
	r := NewRegistry(claimed(t, "svc"), 20000, 30000)
	if _, err := r.Register(protocol.Register{Type: protocol.HTTP, LocalPort: 80}, nil, ""); err != nil {
		t.Fatalf("register http: %v", err)
	}
	if _, err := r.Register(protocol.Register{Type: protocol.TCP, Domain: "svc", LocalPort: 1000}, nil, ""); err != nil {
		t.Fatalf("register tcp: %v", err)
	}
	if got := r.List(); len(got) != 2 {
		t.Fatalf("want 2 tunnels, got %d", len(got))
	}
}
