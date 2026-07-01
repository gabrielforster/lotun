# lotun wire protocol

This document describes the wire format between the `lotun` client and the
`lotund` server. It matches the `internal/protocol` package.

## Transport

The client opens a single connection to the server's control port. For a real
deploy this is a TLS `net.Conn`; for local/testing it may be plaintext. Over
that one connection both sides run [yamux](https://github.com/hashicorp/yamux),
which multiplexes many independent streams:

- The **client** is the yamux client; the **server** is the yamux server.
- **Control stream** — the client *opens* one stream immediately after
  connecting. Every request/reply message (`Auth`, `Register`, `Claim`, …)
  travels on it, one at a time.
- **Data streams** — the server *opens* one stream per inbound public
  connection or HTTP request. The client accepts these, reads a `StreamHeader`,
  then splices the stream to the local port.

```
client ──opens──▶ control stream   (Auth, Register, Claim, Unclaim, List …)
server ──opens──▶ data stream #1    (StreamHeader, then raw bytes) ──▶ localhost:PORT
server ──opens──▶ data stream #2    (StreamHeader, then raw bytes) ──▶ localhost:PORT
```

Because yamux only needs a `net.Conn`, the transport is agnostic to what carries
it (TLS today; a WebSocket could be added later).

## Framing

Every framed value — control messages and stream headers — is written as a
**4-byte big-endian length prefix** followed by exactly that many bytes of JSON.
Reads use `io.ReadFull`, so framing is robust to a reader that delivers bytes in
arbitrary small chunks.

```
+---------------------+------------------------------+
| uint32 length (BE)  | length bytes of JSON payload |
+---------------------+------------------------------+
```

### Control-message envelope

Each control message is a JSON **envelope** that pairs a stable `kind`
discriminator with the message body, so the reader can decode into the right
concrete type:

```json
{ "kind": "register", "payload": { "type": "http", "localPort": 8080, "private": false } }
```

The envelope is what gets length-prefixed and written to the control stream.

## Control messages

Direction: **C→S** = client to server, **S→C** = server to client.

| `kind` | Type | Direction | Fields | Purpose |
| --- | --- | --- | --- | --- |
| `auth` | `Auth` | C→S | `token` | Authenticate the client. Must be the first message. |
| `authok` | `AuthOK` | S→C | *(none)* | Token accepted. |
| `register` | `Register` | C→S | `type` (`http`\|`tcp`), `domain?`, `localPort`, `remotePort?`, `private`, `password?`, `allowIPs?` | Ask the server to expose a local service. |
| `registered` | `Registered` | S→C | `publicURL`, `host`, `port`, `generatedPassword?` | Registration succeeded. |
| `error` | `Error` | S→C | `message` | Any failure (bad token, subdomain/port in use, invalid private config, …). |
| `claim` | `Claim` | C→S | `name` | Reserve a subdomain name. |
| `unclaim` | `Unclaim` | C→S | `name` | Release a reserved subdomain name. |
| `ok` | `OK` | S→C | *(none)* | Generic success (reply to `claim`/`unclaim`). |
| `list` | `ListTunnels` | C→S | *(none)* | Request this token's active tunnels. |
| `tunnellist` | `TunnelList` | S→C | `tunnels[]` (`TunnelInfo`) | Active tunnels. |

### Field notes

**`Register`**
- `type` — `"http"` or `"tcp"`.
- `domain` — omitted ⇒ the server assigns a random `adjective-animal` name.
- `localPort` — the port on the client's machine to forward to.
- `remotePort` — TCP only; omitted (`0`) ⇒ defaults to `localPort`. The public
  port consumers connect to.
- `private` — gate access. For HTTP this enables Basic Auth; for TCP it enables
  the IP allowlist.
- `password` — HTTP only. If `private` HTTP has no password, the server
  generates one and returns it in `generatedPassword`.
- `allowIPs` — TCP only. Source IPs permitted when `private`. `private` TCP with
  no `allowIPs` is rejected with an `error`.

**`Registered`**
- `publicURL` — e.g. `https://brave-otter.yourdomain.com` (HTTP) or
  `tcp://brave-otter.yourdomain.com:25565` (TCP).
- `host` — the bare hostname (`brave-otter.yourdomain.com`).
- `port` — `443` for HTTP tunnels; the allocated public port for TCP.
- `generatedPassword` — present only when a private HTTP tunnel was registered
  without a supplied password.

**`TunnelInfo`** (elements of `TunnelList.tunnels`): `type`, `subdomain`,
`publicURL`, `port`, `localPort`.

## Handshake and lifecycle

1. Client dials the control port and opens the control stream.
2. Client sends `Auth{token}`. The server compares the token in constant time
   (`crypto/subtle`) and replies `AuthOK{}` or `Error{message}` (then closes).
3. Client sends any number of `Register` / `Claim` / `Unclaim` / `ListTunnels`
   messages; the server replies to each on the same stream, in order.
4. As public traffic arrives, the server opens data streams (below).
5. When the control connection drops, the server removes every tunnel owned by
   that session and closes their TCP listeners.

## Data streams

For each inbound public connection (a TCP dial, or one HTTP request via the
reverse proxy), the server opens a fresh yamux stream to the owning client and
writes a **`StreamHeader`** first — itself a length-prefixed JSON frame:

| Field | Type | Purpose |
| --- | --- | --- |
| `tunnelId` | string | Identifies the tunnel the stream belongs to. |
| `localPort` | int | The local port the client should dial. |

After the header, the stream carries raw bytes. The client reads the header,
dials `127.0.0.1:<localPort>`, and splices bytes bidirectionally
(`io.Copy` both ways) until either side closes.

- **HTTP** — the server's reverse-proxy transport opens a stream per request;
  private tunnels enforce Basic Auth (user `lotun`) before the stream is opened.
- **TCP** — one stream per accepted connection; private tunnels check the source
  IP against the allowlist before opening the stream.
