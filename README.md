# lotun

**lotun** is a self-hosted localhost tunnel and port-forwarder — like ngrok, but
you run the server. Expose a local port to the internet through a server you
control, addressed by a subdomain of your own domain. HTTP tunnels, raw TCP
tunnels, and reserved subdomain names, all in one single-tenant binary pair.

```sh
lotun http 8080          # https://brave-otter.yourdomain.com  → localhost:8080
lotun tcp 25565          # brave-otter.yourdomain.com:25565     → localhost:25565
lotun claim myapp        # reserve a stable subdomain name
```

Two binaries: `lotun` (the client CLI you run next to your service) and `lotund`
(the server you self-host). One shared auth token gates the server; there are no
user accounts or billing.

## How it works (in one picture)

```
              (public internet)
Browser ──HTTPS──▶ Caddy ──HTTP──▶ lotund :8000   ─┐
game client ──TCP──────────────▶ lotund :25565   ─┤  control conn (TLS + yamux)
                                                   ├────────────▶ lotun (client)
                                                   │  one stream per request/conn │
                                                   └───────────────────────────────┘ dials localhost:PORT
```

The client dials the server's control port over TLS and multiplexes everything
over a single [yamux](https://github.com/hashicorp/yamux) session. `lotund`
speaks plain HTTP on localhost and lets Caddy terminate wildcard TLS in front.
See [docs/protocol.md](docs/protocol.md) for the wire format and
[docs/DESIGN.md](docs/DESIGN.md) for the full architecture.

## Install

Both commands live in this repo. Build them with the Go toolchain:

```sh
go install github.com/gabrielrocha/lotun/cmd/lotun@latest   # client CLI
go install github.com/gabrielrocha/lotun/cmd/lotund@latest  # server daemon
```

Or from a checkout:

```sh
go install ./cmd/lotun
go install ./cmd/lotund
```

This drops `lotun` and `lotund` into `$(go env GOPATH)/bin`.

## Quick start (local, against `lvh.me`)

`lvh.me` and all its subdomains resolve to `127.0.0.1`, so you can exercise the
whole path — including subdomain routing — without touching DNS.

1. **Run the server** with a plaintext control port and `lvh.me` as the base
   domain. Create `lotund.yaml`:

   ```yaml
   token: dev
   base_domain: lvh.me
   control_addr: ":7000"
   http_addr: ":8000"
   ```

   ```sh
   go run ./cmd/lotund --config lotund.yaml
   ```

2. **Log in** (saves the server address and token to the client config) and
   start something to expose:

   ```sh
   python3 -m http.server 8080          # a local service on :8080
   lotun login --server 127.0.0.1:7000 --token dev
   lotun http 8080
   ```

   `lotun` prints the public URL, e.g. `https://brave-otter.lvh.me`.

3. **Reach it** through the server's HTTP port. Caddy is not running locally, so
   send the `Host` header yourself against `lotund`'s plain HTTP port:

   ```sh
   curl -H 'Host: brave-otter.lvh.me' http://127.0.0.1:8000/
   ```

   You get the page served by `python3 -m http.server` on `:8080`.

4. **Private HTTP tunnel.** `--private` turns on Basic Auth; with no
   `--password` the server generates one and the CLI prints it:

   ```sh
   lotun http 8080 --private
   # prints a generated password, username is always "lotun"
   curl -H 'Host: brave-otter.lvh.me' http://127.0.0.1:8000/                 # 401
   curl -u lotun:<password> -H 'Host: brave-otter.lvh.me' http://127.0.0.1:8000/  # 200
   ```

5. **Reserve a name, then use it.**

   ```sh
   lotun claim myapp
   lotun http 8080 --domain myapp        # now stable at myapp.lvh.me
   ```

6. **TCP tunnel.** Start a local echo/`nc` on `:9000`, then:

   ```sh
   lotun tcp 9000
   # connect directly to the name on that port (no Host header, no Caddy):
   nc myapp.lvh.me 9000
   ```

   With `--private --allow-ip 127.0.0.1`, only connections from `127.0.0.1` are
   accepted; anything else is dropped.

## CLI reference

| Command | Flags | Description |
| --- | --- | --- |
| `lotun login` | `--server host:port`, `--token TOKEN`, `--tls`, `--tls-insecure`, `--default-domain name` | Save server address and token to the client config (`~/.lotun/config.yaml`). |
| `lotun http <port>` | `--domain name`, `--private`, `--password pw` | Expose local HTTP `<port>`. Public at `https://<name>.<base_domain>` (port 443 via Caddy). |
| `lotun tcp <port>` | `--domain name`, `--remote-port N`, `--private`, `--allow-ip IP` (repeatable) | Expose local TCP `<port>`. Reachable at `<name>.<base_domain>:<remote-port>`. `--remote-port` defaults to the local port. |
| `lotun serve` | | Serve every tunnel in the config's `tunnels:` list on one connection, reconnecting on failure. |
| `lotun claim <name>` | | Reserve a stable subdomain name. |
| `lotun unclaim <name>` | | Release a reserved subdomain name. |
| `lotun status` | | List this client's active tunnels and their public URLs. |
| `lotun version` | | Print the version. |

Notes:

- `--domain` omitted → the server assigns a random memorable `adjective-animal`
  name (e.g. `brave-otter`). A `default_domain` in the client config is used
  when set.
- For `tcp`, the default remote port equals the local port, so the service's
  natural port works for consumers (`lotun tcp 25565` → connect on `:25565`).
- On tunnel start, `lotun http`/`lotun tcp` print the public URL (and a
  generated password, if any) and then run until you press Ctrl-C.
- `lotun login --tls` dials the control port over TLS; add `--tls-insecure` for
  a self-signed control certificate. Both are saved to the config, so later
  commands pick them up.

## Many tunnels from one process

`lotun http`/`lotun tcp` expose one port each and exit when the connection
drops. To keep a set of services exposed, declare them in the client config and
run `lotun serve` — it registers them all over a single multiplexed connection
and reconnects with backoff if the server restarts.

```yaml
# ~/.lotun/config.yaml
control_addr: yourdomain.com:7000
token: a-long-random-secret
tls: true

tunnels:
  - type: http
    domain: api          # claim it first: `lotun claim api`
    port: 3000
  - type: tcp
    domain: db
    port: 5432
    remote_port: 25432
    private: true
    allow_ips: ["203.0.113.7"]
```

```sh
lotun serve
# Tunnel ready: https://api.yourdomain.com -> localhost:3000
# Tunnel ready: db.yourdomain.com:25432 -> localhost:5432
# Forwarding traffic. Press Ctrl-C to stop.
```

Per-entry keys mirror the flags: `type` (`http`/`tcp`), `domain`, `port` (local),
`remote_port`, `private`, `password` (http only), `allow_ips` (tcp only). Each
tunnel needs its own subdomain, so `default_domain` is not applied here — give
each entry a `domain:` or omit it for a server-assigned name.

The config carries the token and any passwords, so `lotun login` writes it `0600`
inside a `0700` directory, and re-running `login` leaves `tunnels:` alone. For
running this under systemd, see the
[deployment guide](docs/deploy.md#6-run-the-client-as-a-service).

## Privacy model

Tunnels are **public by default**. `--private` restricts access, differently per
protocol:

- **HTTP** — the server enforces **HTTP Basic Auth** before proxying. `--password X`
  sets the password; without it the server generates a 16-character password and
  the CLI prints it once. The username is always `lotun`.
- **TCP** — raw clients (a game client, `psql`) can't send a password, so
  `--private` uses an **IP allowlist**. Pass one or more `--allow-ip IP`; only
  source IPs on the list may connect. `--private` on a TCP tunnel with no
  `--allow-ip` is an error, and `--password` does not apply to TCP.

## Self-hosting

To run this for real on your own domain, follow the
**[deployment guide](docs/deploy.md)** — it covers wildcard DNS, a Caddyfile for
wildcard TLS, firewall rules, the full `lotund.yaml`, and running `lotund` as a
systemd service.

> **Control-channel note:** the control port carries the shared token, and it
> is plaintext unless you configure it. Either set `control_tls_cert`/
> `control_tls_key` on the server and log in with `--tls`, or keep the port off
> the public internet (WireGuard/Tailscale/SSH tunnel) — see
> [Control-channel security](docs/deploy.md#control-channel-security).

## Documentation

- [docs/deploy.md](docs/deploy.md) — production self-hosting guide: DNS, Caddy, firewall, `lotund.yaml`, systemd, and control-channel security.
- [docs/protocol.md](docs/protocol.md) — control-channel and data-stream wire format.
- [docs/DESIGN.md](docs/DESIGN.md) — architecture and design decisions.
