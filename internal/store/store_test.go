package store

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestClaimReleaseIsClaimed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "claims.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.IsClaimed("myapp") {
		t.Fatal("fresh store should not have myapp")
	}
	if err := s.Claim("myapp"); err != nil {
		t.Fatal(err)
	}
	if !s.IsClaimed("myapp") {
		t.Fatal("myapp should be claimed")
	}
	if err := s.Claim("myapp"); err != ErrClaimed {
		t.Fatalf("want ErrClaimed, got %v", err)
	}
	if err := s.Release("myapp"); err != nil {
		t.Fatal(err)
	}
	if s.IsClaimed("myapp") {
		t.Fatal("myapp should be free after release")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "claims.json")
	s1, _ := Open(p)
	_ = s1.Claim("stable")
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.IsClaimed("stable") {
		t.Fatal("claim did not persist to disk")
	}
}

func TestConcurrentClaims(t *testing.T) { // run with -race
	s, _ := Open(filepath.Join(t.TempDir(), "claims.json"))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Claim("x") }()
	}
	wg.Wait()
	if !s.IsClaimed("x") {
		t.Fatal("x should be claimed exactly once")
	}
}
