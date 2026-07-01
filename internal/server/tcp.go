package server

import (
	"fmt"
	"io"
	"net"
)

// ponytail: one net.Listener per tcp tunnel — simplest correct model; a shared SNI/port-mux only if listener count ever matters.

// startTCPTunnel binds the public listener for a tcp tunnel and spawns its
// accept loop. The listener is tracked so control-connection cleanup can close
// it when the owning session goes away.
func (s *Server) startTCPTunnel(tn *Tunnel) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", tn.Port))
	if err != nil {
		return err
	}
	s.tcpMu.Lock()
	s.tcpListeners[tn.Port] = ln
	s.tcpMu.Unlock()

	go s.acceptTCP(tn, ln)
	return nil
}

// acceptTCP accepts public connections for tn until its listener is closed. For
// private tunnels it enforces the source-IP allowlist; each accepted connection
// is spliced to a fresh yamux data stream.
func (s *Server) acceptTCP(tn *Tunnel, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed (session gone or shutdown)
		}
		if tn.Private && !ipAllowed(conn.RemoteAddr().String(), tn.AllowIPs) {
			conn.Close()
			continue
		}
		go spliceTCP(tn, conn)
	}
}

// spliceTCP opens a yamux data stream to the client (writing the StreamHeader
// first) and copies bytes in both directions until either side closes. This
// server OPENS the data stream; the client ACCEPTS it.
func spliceTCP(tn *Tunnel, conn net.Conn) {
	defer conn.Close()

	stream, err := openDataStream(tn)
	if err != nil {
		return
	}
	defer stream.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(stream, conn)
		stream.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(conn, stream)
		conn.Close()
		done <- struct{}{}
	}()
	<-done
}
