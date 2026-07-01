// Package protocol defines the wire format for the lotun control channel and
// data-stream headers. It is pure and has no I/O dependencies beyond the
// standard library, making it fully unit-testable.
//
// Every framed value (control messages and stream headers) is written as a
// 4-byte big-endian length prefix followed by that many JSON bytes. Control
// messages are wrapped in an envelope of the form {"kind":...,"payload":...}
// so that ReadMessage can decode into the correct concrete type.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// TunnelType identifies the kind of tunnel being registered.
type TunnelType string

// Supported tunnel types.
const (
	HTTP TunnelType = "http"
	TCP  TunnelType = "tcp"
)

// ErrUnknownKind is returned by ReadMessage when the envelope carries a kind
// that does not correspond to any known message type.
var ErrUnknownKind = errors.New("protocol: unknown message kind")

// Auth authenticates a client to the server using a token.
type Auth struct {
	Token string `json:"token"`
}

// AuthOK is the server's acknowledgement of a successful Auth.
type AuthOK struct{}

// Register requests that the server expose a local service.
type Register struct {
	Type       TunnelType `json:"type"`
	Domain     string     `json:"domain,omitempty"` // "" => server assigns
	LocalPort  int        `json:"localPort"`
	RemotePort int        `json:"remotePort,omitempty"` // tcp; 0 => defaults to LocalPort
	Private    bool       `json:"private"`
	Password   string     `json:"password,omitempty"` // http only
	AllowIPs   []string   `json:"allowIPs,omitempty"` // tcp only
}

// Registered is the server's reply to a successful Register.
type Registered struct {
	PublicURL         string `json:"publicURL"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	GeneratedPassword string `json:"generatedPassword,omitempty"`
}

// Error carries a human-readable failure message from the server.
type Error struct {
	Message string `json:"message"`
}

// Claim requests ownership of a subdomain name.
type Claim struct {
	Name string `json:"name"`
}

// Unclaim releases ownership of a subdomain name.
type Unclaim struct {
	Name string `json:"name"`
}

// OK is a generic success acknowledgement (e.g. for Claim/Unclaim).
type OK struct{}

// ListTunnels requests the active tunnels associated with the client's token.
type ListTunnels struct{}

// TunnelInfo describes a single active tunnel.
type TunnelInfo struct {
	Type      TunnelType `json:"type"`
	Subdomain string     `json:"subdomain"`
	PublicURL string     `json:"publicURL"`
	Port      int        `json:"port"`
	LocalPort int        `json:"localPort"`
}

// TunnelList is the server's reply to ListTunnels.
type TunnelList struct {
	Tunnels []TunnelInfo `json:"tunnels"`
}

// Message is implemented by every control-channel message. Kind returns the
// stable string discriminator used in the wire envelope.
type Message interface {
	Kind() string
}

// Kind returns the wire discriminator for Auth.
func (Auth) Kind() string { return "auth" }

// Kind returns the wire discriminator for AuthOK.
func (AuthOK) Kind() string { return "authok" }

// Kind returns the wire discriminator for Register.
func (Register) Kind() string { return "register" }

// Kind returns the wire discriminator for Registered.
func (Registered) Kind() string { return "registered" }

// Kind returns the wire discriminator for Error.
func (Error) Kind() string { return "error" }

// Kind returns the wire discriminator for Claim.
func (Claim) Kind() string { return "claim" }

// Kind returns the wire discriminator for Unclaim.
func (Unclaim) Kind() string { return "unclaim" }

// Kind returns the wire discriminator for OK.
func (OK) Kind() string { return "ok" }

// Kind returns the wire discriminator for ListTunnels.
func (ListTunnels) Kind() string { return "list" }

// Kind returns the wire discriminator for TunnelList.
func (TunnelList) Kind() string { return "tunnellist" }

// envelope is the on-wire wrapper that pairs a message kind with its JSON body.
type envelope struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// writeFrame writes b as a 4-byte big-endian length prefix followed by b.
func writeFrame(w io.Writer, b []byte) error {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(b)))
	if _, err := w.Write(length[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readFrame reads one length-prefixed frame. It uses io.ReadFull so that it
// works correctly even when the underlying reader delivers bytes in small,
// arbitrary chunks.
func readFrame(r io.Reader) ([]byte, error) {
	var length [4]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(length[:])
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteMessage encodes m into an envelope and writes it as a single
// length-prefixed frame.
func WriteMessage(w io.Writer, m Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	frame, err := json.Marshal(envelope{Kind: m.Kind(), Payload: payload})
	if err != nil {
		return err
	}
	return writeFrame(w, frame)
}

// ReadMessage reads one length-prefixed frame and decodes it into the concrete
// message type indicated by the envelope's kind. It returns ErrUnknownKind for
// an unrecognized kind.
func ReadMessage(r io.Reader) (Message, error) {
	frame, err := readFrame(r)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		return nil, err
	}
	var m Message
	switch env.Kind {
	case "auth":
		m = new(Auth)
	case "authok":
		m = new(AuthOK)
	case "register":
		m = new(Register)
	case "registered":
		m = new(Registered)
	case "error":
		m = new(Error)
	case "claim":
		m = new(Claim)
	case "unclaim":
		m = new(Unclaim)
	case "ok":
		m = new(OK)
	case "list":
		m = new(ListTunnels)
	case "tunnellist":
		m = new(TunnelList)
	default:
		return nil, ErrUnknownKind
	}
	if err := json.Unmarshal(env.Payload, m); err != nil {
		return nil, err
	}
	// Return by value to match the value-receiver Kind implementations and the
	// concrete types callers type-assert against.
	return derefMessage(m), nil
}

// derefMessage converts the pointer used for JSON decoding back into the value
// type that implements Message.
func derefMessage(m Message) Message {
	switch v := m.(type) {
	case *Auth:
		return *v
	case *AuthOK:
		return *v
	case *Register:
		return *v
	case *Registered:
		return *v
	case *Error:
		return *v
	case *Claim:
		return *v
	case *Unclaim:
		return *v
	case *OK:
		return *v
	case *ListTunnels:
		return *v
	case *TunnelList:
		return *v
	default:
		return m
	}
}

// StreamHeader is written by the server on each new proxy stream so the client
// knows which local port to dial for the given tunnel.
type StreamHeader struct {
	TunnelID  string `json:"tunnelId"`
	LocalPort int    `json:"localPort"`
}

// WriteStreamHeader writes h as a single length-prefixed JSON frame.
func WriteStreamHeader(w io.Writer, h StreamHeader) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return writeFrame(w, b)
}

// ReadStreamHeader reads one length-prefixed JSON frame into a StreamHeader.
func ReadStreamHeader(r io.Reader) (StreamHeader, error) {
	var h StreamHeader
	frame, err := readFrame(r)
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(frame, &h)
	return h, err
}
