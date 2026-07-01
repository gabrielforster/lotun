# lotun — localhost tunnel & port forwarding (ngrok/pinggy-style)

## Context

We're building `lotun` from scratch (empty repo). It lets a user expose a local
port to the internet through a self-hosted server:

- `lotun http 8080` → public `https://<name>.yourdomain.com` → `localhost:8080`
- `lotun tcp 25565` → public `<name>.yourdomain.com:25565` → `localhost:25565`
- `lotun claim myapp` → reserve a stable subdomain name

**Tunnels are public by default.** `--private` restricts access; the mechanism
differs by protocol (see below). `--domain NAME` picks the subdomain; without it
a random memorable name (`adjective-animal`, e.g. `brave-otter`) is generated.

Decisions locked with the user:
- **Language:** Go (single static binary, best-in-class networking, standard for this class of tool).
- **Topology:** Self-hosted, single-tenant. One shared auth token gates the server. No user accounts, no billing.
- **v1 scope:** HTTP tunnels + TCP tunnels + subdomain claim + private tunnels, all in v1.
- **Transport:** yamux stream multiplexing over a single TLS `net.Conn`. Chosen because yamux works over any `net.Conn` → fully in-memory testable via `net.Pipe()`, and multiplexes many concurrent tunnel streams over one connection. A WebSocket transport can be added later since yamux only needs a `net.Conn`.
- **Public TLS/DNS:** Caddy reverse proxy in front. `lotund` speaks plain HTTP internally on localhost; Caddy handles wildcard TLS (`*.yourdomain.com`) and HTTP/2.
- **Config:** `spf13/viper` from the start (pairs with cobra; env + file + flag layering).

Development uses **TDD** (superpowers:test-driven-development) throughout, with well-documented, isolated packages.

## Routing model (the important part)

Wildcard DNS `*.yourdomain.com` → the server's IP. Both tunnel types are addressed
by a **subdomain name**, but they route differently on the server:

- **HTTP** is routed by the `Host` header on the shared public HTTP port (443/80 via Caddy). One HTTP tunnel per subdomain.
- **TCP** is routed by **public port**. The consumer connects to `<name>.yourdomain.com:<port>`; DNS just resolves the name to the server IP, and the port is what actually selects the tunnel on the server. The default remote port = the local port, i.e. the service's natural port (`lotun tcp 25565` → consumers use `:25565`, `lotun http`→ consumers use `:443`). Users override with `--remote-port`. This is why the same memorable domain works for a browser (443) and a Minecraft client (25565) simultaneously — one name, protocol-default ports.

Registry is keyed by **(subdomain, port)**. A subdomain can host several tunnels
on different ports (HTTP on the http port + TCP on 25565 + TCP on 5432). Registering
a second tunnel on an already-active `(subdomain, port)` is rejected. For TCP, the
requested public port must be free and within the server's configured allowed range.

## Privacy model

Public by default. `--private` gates access:

- **HTTP private:** server enforces **HTTP Basic Auth** before proxying. `--password X` sets the password; if `--private` is given without `--password`, the server generates a random one and the CLI prints it. (Username is `lotun`.)
- **TCP private:** password auth is impossible for raw clients (Minecraft/psql send no password), so `--private` on a TCP tunnel uses an **IP allowlist** — the server only accepts connections whose source IP is in `--allow-ip` (repeatable). `--private` TCP with no `--allow-ip` is an error. `--password` is ignored/rejected for TCP.

## Architecture

Two binaries, shared internal packages.

```
              (public internet)
Browser ──HTTPS──▶ Caddy ──HTTP──▶ lotund :80   ─┐
game client ──TCP──────────────▶ lotund :25565 ─┤   control conn (TLS + yamux)
                                                 ├──────────────────────────▶ lotun (client)
                                                 │   opens a stream per conn/request  │
                                                 └────────────────────────────────────┘ dials localhost:PORT
```

- **`lotun` (client CLI):** dials the control port over TLS, authenticates, registers tunnels, then for each inbound stream from the server dials the local port and pipes bytes.
- **`lotund` (server):** listeners — control port (clients connect), public HTTP port (vhost-routed by `Host`), and one TCP listener per active tcp tunnel (bound to that tunnel's public port). Maintains the `(subdomain, port)` registry mapping to the yamux session that can open a stream to the owning client, plus each tunnel's privacy settings.

### Data flow

- **HTTP:** inbound request → look up tunnel by `Host` subdomain → if private, enforce Basic Auth → `httputil.ReverseProxy` whose `Transport.DialContext` opens a fresh yamux stream to the owning client → client dials `localhost:port`, proxies, response streams back.
- **TCP:** inbound conn on the tunnel's public port → if private, check source IP against allowlist → open yamux stream to client → bidirectional `io.Copy` splice → client dials `localhost:port`.

### Control protocol (over a dedicated yamux control stream)

Length-prefixed JSON messages (simple, debuggable, unit-testable):
- `Auth{token}` → `AuthOK{}` | `Error{msg}` (constant-time token compare).
- `Register{type: http|tcp, domain?, localPort, remotePort?, private, password?, allowIPs?}` →
  `Registered{publicURL, host, port, generatedPassword?}` | `Error{msg}`.
  Errors: subdomain-in-use for that port, tcp port unavailable/out-of-range, `--private` tcp without allow-ips, bad domain name.
  - `domain` omitted → server assigns a random memorable name.
  - `remotePort` omitted (tcp) → defaults to `localPort`.
  - `generatedPassword` returned only when private http had no `--password`.
- Data streams: server opens a new proxy stream per inbound conn/request, tagged with the tunnel id in a small stream header so the client knows which local port to dial.

### Persistence — subdomain claims

Single-tenant, so "claim" reserves stable names and prevents random-assignment collisions.
- `store` package with a small interface, backed by a **JSON file guarded by a mutex** in the server's data dir. `Claim/Release/IsClaimed/List`.
- `// ponytail: JSON file store — single-tenant, low write volume. Swap for SQLite when multi-tenant.`

### Auth & config

- **Server auth:** one shared token in server config, compared with `crypto/subtle`.
- **Server config:** control addr, control TLS cert/key (optional; plaintext for local/testing), public HTTP addr, base domain, TCP allowed-port range, auth token, data dir.
- **Client config (`~/.lotun/config.yaml`):** control addr, token, default domain. Written by `lotun login`.
- Loaded via **viper** (file + env + flag binding) in both binaries; cobra flags bound to viper keys.

## CLI surface

- `lotun login --server host:7000 --token TOKEN` — save server + token to client config.
- `lotun http <port> [--domain name] [--private] [--password pw]`
- `lotun tcp <port> [--domain name] [--remote-port N] [--private] [--allow-ip IP ...]`
- `lotun claim <name>` / `lotun unclaim <name>`
- `lotun status` — this client's active tunnels + public URLs.
- `lotun version`.
- Built with `cobra`/`pflag` + `viper`. On tunnel start, prints the public URL (and generated password if any), then streams a request log until Ctrl-C.

## Proposed layout

```
lotun/
  go.mod
  cmd/lotun/main.go        # client CLI entrypoint (cobra + viper)
  cmd/lotund/main.go       # server entrypoint
  internal/protocol/       # message types + length-prefixed JSON framing + stream header
  internal/client/         # connect, auth, register, inbound-stream → localhost dialer
  internal/server/
    server.go              # wires listeners together
    registry.go            # (subdomain,port) → session; tcp port allocation; privacy metadata
    http.go                # vhost router + Basic Auth + ReverseProxy-over-yamux transport
    tcp.go                 # per-tunnel TCP listener + IP allowlist + splice
  internal/store/          # claims persistence (JSON file + interface)
  internal/config/         # viper-backed client + server config
  internal/names/          # random memorable name generator (adjective+animal wordlists)
  README.md                # architecture, install, self-host (Caddyfile example), usage
  docs/                    # protocol.md, deploy.md
```

Key deps: `github.com/hashicorp/yamux`, `github.com/spf13/cobra`, `github.com/spf13/viper`. Stdlib for the rest (`net`, `net/http`, `httputil`, `crypto/tls`, `crypto/subtle`, `crypto/rand`, `io`, `encoding/json`).

## TDD build order (write test → red → green → refactor at each step)

1. **`protocol`** — round-trip encode/decode each message incl. new fields; framing across split reads; stream header parse.
2. **`names`** — generated names match `adjective-animal`, drawn from the lists; reasonable distribution.
3. **`store`** — claim/release/idempotency/persistence-across-reload; concurrent claims under `-race`.
4. **`config`** — viper load with file, env override, and flag override precedence; missing-file defaults.
5. **`server/registry`** — register http (unique/duplicate per subdomain), register tcp (default remote-port = local, specific `--remote-port`, out-of-range, port-in-use, duplicate `(subdomain,port)`), random-name assignment when domain omitted, cleanup on disconnect.
6. **Control handshake** — server+client over `net.Pipe()` + yamux: auth success/failure, register success/error. No real sockets.
7. **HTTP tunnel (in-process integration)** — `httptest` upstream; `lotund` on `:0`; real client over loopback; assert a request with `Host: name.base` reaches the upstream. Then a **private** variant: 401 without creds, 200 with the password.
8. **TCP tunnel (in-process integration)** — local echo server; assert echo round-trips through the allocated public port. Then a **private** variant: connection from a non-allowlisted IP is refused, allowlisted succeeds.
9. **CLI wiring** — command/flag parsing & validation (e.g. `--private` tcp requires `--allow-ip`); thin `main`.

All tests run under `go test -race ./...`.

## Verification (end to end)

- `go test -race ./...` green; `go vet ./...` and `gofmt` clean.
- Manual smoke (documented in README), using `lvh.me` (resolves `*.lvh.me`→127.0.0.1):
  1. `go run ./cmd/lotund -config lotund.yaml` (plaintext control, base domain `lvh.me`).
  2. `lotun login --server 127.0.0.1:7000 --token dev`, then `lotun http 8080` against `python -m http.server 8080`.
  3. `curl -H 'Host: <name>.lvh.me' http://127.0.0.1:80/` returns the local page.
  4. `lotun http 8080 --private` → prints a generated password; same curl → 401; with `-u lotun:<pw>` → 200.
  5. `lotun claim myapp`; `lotun http 8080 --domain myapp`; confirm stable `myapp.lvh.me`.
  6. `lotun tcp 9000` against a local echo/`nc`; connect to `myapp.lvh.me:9000` and confirm round-trip. Then `--private --allow-ip 127.0.0.1` and confirm a non-allowed IP is refused.
- README documents production: Caddyfile snippet for `*.yourdomain.com` → `lotund` HTTP port, plus opening the control port and the TCP port range.

## Deliberately out of scope for v1

- Multi-tenant accounts, quotas, billing, web dashboard.
- Built-in ACME/wildcard TLS (Caddy handles it).
- WebSocket transport (the yamux-over-`net.Conn` design leaves the seam for it).
- Request-inspection UI.
