// Package netutil holds the small networking helpers shared by the lotun
// client and server.
package netutil

import (
	"io"
	"net"
	"sync"
)

// halfCloser is implemented by connections that can signal end-of-stream on
// their write side while still delivering reads. *net.TCPConn provides
// CloseWrite; *yamux.Stream has no CloseWrite but its Close sends a FIN and
// leaves the read side open, which is the same thing, so it falls through to
// the Close branch below.
type halfCloser interface{ CloseWrite() error }

// Splice copies bytes between a and b in both directions and returns once both
// directions are done.
//
// When one direction reaches EOF it half-closes the destination rather than
// closing the whole connection, so a peer that has finished sending but is
// still waiting to receive — `printf ping | nc host port`, any client that
// shuts down its write side after the request — still gets the reply. Closing
// outright here drops that reply on the floor.
//
// Splice does not close either side; the caller owns their lifetimes.
func Splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyThenHalfClose(a, b) }()
	go func() { defer wg.Done(); copyThenHalfClose(b, a) }()
	wg.Wait()
}

// copyThenHalfClose copies src into dst, then signals end-of-stream to dst.
func copyThenHalfClose(dst, src net.Conn) {
	io.Copy(dst, src)
	if hc, ok := dst.(halfCloser); ok {
		hc.CloseWrite()
		return
	}
	// yamux streams land here: Close sends a FIN and keeps reads alive.
	dst.Close()
}
