package names

import (
	"math/rand"
	"regexp"
	"testing"
)

var namePattern = regexp.MustCompile(`^[a-z]+-[a-z]+$`)

func TestGenerateShape(t *testing.T) {
	for i := 0; i < 200; i++ {
		n := Generate()
		if !namePattern.MatchString(n) {
			t.Fatalf("name %q does not match adjective-animal", n)
		}
	}
}

func TestGenerateFromIsDeterministic(t *testing.T) {
	a := GenerateFrom(rand.New(rand.NewSource(42)))
	b := GenerateFrom(rand.New(rand.NewSource(42)))
	if a != b {
		t.Fatalf("same seed gave different names: %q vs %q", a, b)
	}
}

func TestGenerateHasVariety(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		seen[Generate()] = true
	}
	if len(seen) < 20 {
		t.Fatalf("only %d distinct names in 100 draws — too low", len(seen))
	}
}
