package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	cases := []Message{
		Auth{Token: "sekret"},
		AuthOK{},
		Register{Type: TCP, Domain: "myapp", LocalPort: 25565, RemotePort: 25565, Private: true, AllowIPs: []string{"1.2.3.4"}},
		Registered{PublicURL: "https://myapp.lvh.me", Host: "myapp.lvh.me", Port: 443, GeneratedPassword: "pw"},
		Error{Message: "boom"},
		Claim{Name: "myapp"},
		Unclaim{Name: "myapp"},
		OK{},
		ListTunnels{},
		TunnelList{Tunnels: []TunnelInfo{{Type: HTTP, Subdomain: "myapp", PublicURL: "https://myapp.lvh.me", Port: 443, LocalPort: 8080}}},
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteMessage(&buf, want); err != nil {
			t.Fatal(err)
		}
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind() != want.Kind() {
			t.Fatalf("kind %q != %q", got.Kind(), want.Kind())
		}
	}
}

// Framing must survive a reader that returns one byte at a time.
func TestReadMessageAcrossSplitReads(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteMessage(&buf, Register{Type: HTTP, LocalPort: 8080})
	got, err := ReadMessage(iotest_oneByte(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := got.(Register)
	if !ok || r.LocalPort != 8080 || r.Type != HTTP {
		t.Fatalf("bad decode: %#v", got)
	}
}

func TestStreamHeaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStreamHeader(&buf, StreamHeader{TunnelID: "t1", LocalPort: 8080}); err != nil {
		t.Fatal(err)
	}
	h, err := ReadStreamHeader(&buf)
	if err != nil || h.TunnelID != "t1" || h.LocalPort != 8080 {
		t.Fatalf("bad: %#v err=%v", h, err)
	}
}

// tiny helper: reader yielding one byte per Read
type oneByte struct{ b []byte }

func iotest_oneByte(b []byte) io.Reader { return &oneByte{b} }
func (o *oneByte) Read(p []byte) (int, error) {
	if len(o.b) == 0 {
		return 0, io.EOF
	}
	p[0] = o.b[0]
	o.b = o.b[1:]
	return 1, nil
}
