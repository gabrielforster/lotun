package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gabrielrocha/lotun/internal/config"
)

func TestSubdomainFromHost(t *testing.T) {
	if s, ok := subdomainFromHost("myapp.lvh.me:8000", "lvh.me"); !ok || s != "myapp" {
		t.Fatalf("myapp.lvh.me:8000 => (%q, %v), want (myapp, true)", s, ok)
	}
	if s, ok := subdomainFromHost("myapp.lvh.me", "lvh.me"); !ok || s != "myapp" {
		t.Fatalf("myapp.lvh.me => (%q, %v), want (myapp, true)", s, ok)
	}
	if _, ok := subdomainFromHost("lvh.me", "lvh.me"); ok {
		t.Fatal("base with no sub should be false")
	}
	if _, ok := subdomainFromHost("lvh.me:8000", "lvh.me"); ok {
		t.Fatal("base with port and no sub should be false")
	}
	if _, ok := subdomainFromHost("example.com", "lvh.me"); ok {
		t.Fatal("unrelated host should be false")
	}
}

func TestBasicAuthOK(t *testing.T) {
	const pw = "s3cret"

	good, _ := http.NewRequest("GET", "/", nil)
	good.SetBasicAuth("lotun", pw)
	if !basicAuthOK(good, pw) {
		t.Fatal("correct lotun:<pw> creds should pass")
	}

	badPass, _ := http.NewRequest("GET", "/", nil)
	badPass.SetBasicAuth("lotun", "wrong")
	if basicAuthOK(badPass, pw) {
		t.Fatal("wrong password should fail")
	}

	badUser, _ := http.NewRequest("GET", "/", nil)
	badUser.SetBasicAuth("someoneelse", pw)
	if basicAuthOK(badUser, pw) {
		t.Fatal("wrong username should fail")
	}

	none, _ := http.NewRequest("GET", "/", nil)
	if basicAuthOK(none, pw) {
		t.Fatal("missing creds should fail")
	}
}

func TestIPAllowed(t *testing.T) {
	if !ipAllowed("203.0.113.7:5555", []string{"203.0.113.7"}) {
		t.Fatal("in-list IP should be allowed")
	}
	if ipAllowed("198.51.100.9:5555", []string{"203.0.113.7"}) {
		t.Fatal("out-of-list IP should be denied")
	}
	if !ipAllowed("203.0.113.7:5555", nil) {
		t.Fatal("empty list should allow all (public)")
	}
	if !ipAllowed("203.0.113.7:5555", []string{}) {
		t.Fatal("empty list should allow all (public)")
	}
}

// TestNewRunBindsAndReportsAddr checks that New succeeds and Run binds the
// control and HTTP listeners on :0, reporting concrete addresses, then drains
// cleanly when the context is cancelled.
func TestNewRunBindsAndReportsAddr(t *testing.T) {
	cfg := config.ServerConfig{
		ControlAddr: "127.0.0.1:0",
		HTTPAddr:    "127.0.0.1:0",
		BaseDomain:  "lvh.me",
		TCPPortMin:  20000,
		TCPPortMax:  30000,
		Token:       "test-token",
	}
	srv, err := New(cfg, memStore(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()

	// Wait for the listeners to bind so the addresses resolve.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if srv.ControlAddr() != "" && srv.HTTPAddr() != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listeners did not bind within deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if srv.ControlAddr() == "127.0.0.1:0" || srv.ControlAddr() == "" {
		t.Fatalf("ControlAddr not resolved: %q", srv.ControlAddr())
	}
	if srv.HTTPAddr() == "127.0.0.1:0" || srv.HTTPAddr() == "" {
		t.Fatalf("HTTPAddr not resolved: %q", srv.HTTPAddr())
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
