// Command lotun is the user-facing client CLI for lotun. It authenticates to a
// lotun control server and exposes local HTTP/TCP services through it.
package main

import "os"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
