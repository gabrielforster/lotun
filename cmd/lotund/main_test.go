package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielrocha/lotun/internal/config"
)

// testConfig returns a server config bound to ephemeral ports with a data dir
// under dir (deliberately not pre-created, so run must create it).
func testConfig(dir string) config.ServerConfig {
	return config.ServerConfig{
		ControlAddr: "127.0.0.1:0",
		HTTPAddr:    "127.0.0.1:0",
		BaseDomain:  "lvh.me",
		Token:       "dev",
		DataDir:     filepath.Join(dir, "data"),
		TCPPortMin:  20000,
		TCPPortMax:  20100,
	}
}

func TestRunCreatesDataDirBindsAndStopsOnCancel(t *testing.T) {
	cfg := testConfig(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- run(ctx, cfg) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(cfg.DataDir); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not create the data dir")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after context cancel")
	}
}

func TestRunRejectsUnusableDataDir(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "data")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	err := run(context.Background(), testConfig(dir))
	if err == nil || !strings.Contains(err.Error(), "create data dir") {
		t.Fatalf("run with a file where the data dir goes = %v, want create data dir error", err)
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	cfg := testConfig(t.TempDir())
	cfg.ControlAddr = ln.Addr().String() // already taken

	if err := run(context.Background(), cfg); err == nil {
		t.Fatal("run on an occupied control port must error")
	}
}

func TestRootCmdReportsBadConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lotund.yaml")
	if err := os.WriteFile(path, []byte("control_addr: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", path})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("root cmd with a malformed config = %v, want load config error", err)
	}
}

func TestRootCmdRejectsUnknownFlag(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"-config", "lotund.yaml"}) // single dash: not a shorthand
	cmd.SetOut(os.NewFile(0, os.DevNull))
	cmd.SetErr(os.NewFile(0, os.DevNull))
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("single-dash -config must be rejected (docs must use --config)")
	}
}
