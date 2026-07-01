package config

import (
	"os"
	"path/filepath"
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
	want := ClientConfig{ControlAddr: "127.0.0.1:7000", Token: "dev", DefaultDomain: "myapp"}
	if err := SaveClient(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: %#v != %#v", got, want)
	}
}

func TestLoadClientMissingFileIsNotError(t *testing.T) {
	_, err := LoadClient(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing client config should not error: %v", err)
	}
}
