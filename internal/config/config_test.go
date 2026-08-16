package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadServerDefaults(t *testing.T) {
	c, err := LoadServer("")
	if err != nil {
		t.Fatal(err)
	}
	if c.ControlAddr != ":7000" || c.TCPPortMin != 20000 || c.TCPPortMax != 30000 {
		t.Fatalf("defaults not applied: %#v", c)
	}
}

func TestLoadServerFileThenEnvOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lotund.yaml")
	os.WriteFile(p, []byte("token: fromfile\nbase_domain: lvh.me\n"), 0o600)
	t.Setenv("LOTUND_TOKEN", "fromenv")
	c, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseDomain != "lvh.me" {
		t.Fatalf("file value lost: %#v", c)
	}
	if c.Token != "fromenv" {
		t.Fatalf("env should override file: %q", c.Token)
	}
}

func TestSaveThenLoadClientRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg", "config.yaml")
	want := ClientConfig{
		ControlAddr:   "127.0.0.1:7000",
		Token:         "dev",
		DefaultDomain: "myapp",
		TLS:           true,
		TLSInsecure:   true,
		Tunnels: []TunnelConfig{
			{Type: "http", Domain: "api", Port: 3000, Private: true, Password: "pw"},
			{Type: "tcp", Domain: "db", Port: 5432, RemotePort: 25432, Private: true, AllowIPs: []string{"1.2.3.4"}},
		},
	}
	if err := SaveClient(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

// The client config holds the auth token and tunnel passwords, so it must not
// be group/world readable.
func TestSaveClientPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg", "config.yaml")
	if err := SaveClient(p, ClientConfig{ControlAddr: "a:1", Token: "dev"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config perms = %o, want 600", perm)
	}
}

func TestLoadClientMissingFileIsNotError(t *testing.T) {
	_, err := LoadClient(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing client config should not error: %v", err)
	}
}
