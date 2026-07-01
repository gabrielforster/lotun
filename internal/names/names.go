// Package names generates memorable "adjective-animal" subdomain names
// for tunnels registered without an explicit --domain.
package names

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"math/rand"
	"strings"
	"sync"
)

var adjectives = []string{
	"brave", "calm", "clever", "cosmic", "crimson", "dapper", "eager",
	"fuzzy", "gentle", "golden", "happy", "jolly", "keen", "lucky",
	"mellow", "nimble", "plucky", "quiet", "rapid", "shiny", "silent",
	"snappy", "spry", "sunny", "swift", "tidy", "vivid", "witty",
	"zany", "zesty",
}

var animals = []string{
	"otter", "falcon", "badger", "panda", "lynx", "heron", "marten",
	"gecko", "walrus", "puffin", "beaver", "ferret", "raven", "moose",
	"koala", "bison", "cobra", "dingo", "egret", "finch", "gopher",
	"hare", "ibex", "jackal", "kestrel", "lemur", "meerkat", "newt",
	"osprey", "quokka",
}

var (
	mu  sync.Mutex
	rng = rand.New(rand.NewSource(seed()))
)

// seed reads 8 bytes from crypto/rand and folds them into an int64 seed.
func seed() int64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic("names: unable to read crypto/rand seed: " + err.Error())
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}

// Generate returns a random name like "brave-otter" using the package's
// own randomness source (crypto-seeded). It is safe for concurrent use.
func Generate() string {
	mu.Lock()
	defer mu.Unlock()
	return GenerateFrom(rng)
}

// GenerateFrom returns a random "adjective-animal" name using the caller's
// rand source. Used by tests for determinism.
//
// ponytail: 30x30 wordlist = 900 combos, fine single-tenant. Add a numeric suffix if collisions ever bite.
func GenerateFrom(r *rand.Rand) string {
	return strings.Join([]string{
		adjectives[r.Intn(len(adjectives))],
		animals[r.Intn(len(animals))],
	}, "-")
}
