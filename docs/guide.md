# lotun guide — install, host, use

The complete path from an empty machine to a local service reachable from the
internet. Three parts, in order:

1. [Install](#1-install) — build the two binaries.
2. [Host](#2-host-the-server) — get `lotund` running, locally or on a real domain.
3. [Use](#3-use-the-client) — expose ports with `lotun`.

Then: [config reference](#4-config-reference) and
[troubleshooting](#5-troubleshooting).

## How the pieces fit

`lotun` is two binaries:

| Binary | Runs on | Job |
| --- | --- | --- |
| `lotund` | your public server | Accepts client connections, owns the public listeners, routes inbound traffic. |
| `lotun` | your laptop / any machine with a service | Dials the server, registers tunnels, forwards traffic to `localhost`. |

The client holds one long-lived connection **outbound** to the server and
multiplexes everything over it, so the machine running `lotun` needs no open
ports and no public IP. The server needs a domain, a wildcard DNS record, and
open ports.

It is **single-tenant**: one shared token gates the whole server. Anyone with
the token can register tunnels. There are no accounts.

## 1. Install

Requires Go 1.25.5 or newer.

```sh
go install github.com/gabrielrocha/lotun/cmd/lotun@latest    # client
go install github.com/gabrielrocha/lotun/cmd/lotund@latest   # server
```

Both land in `$(go env GOPATH)/bin` — add it to `PATH` if it isn't already:

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
lotun version
```

From a checkout instead:

```sh
git clone https://github.com/gabrielforster/lotun && cd lotun
go build -o lotun  ./cmd/lotun
go build -o lotund ./cmd/lotund
```

On the public server you only need `lotund`; on your workstation you only need
`lotun`. Build `lotund` for the server's platform if you cross-compile:

```sh
GOOS=linux GOARCH=amd64 go build -o lotund ./cmd/lotund
```

## 2. Host the server

Start with the local run to confirm everything works, then do the real one.

### 2a. Locally, in five minutes

`lvh.me` and every subdomain of it resolve to `127.0.0.1`, so you can exercise
subdomain routing without touching DNS or TLS.

Write `lotund.yaml`:

```yaml
token: dev
base_domain: lvh.me
control_addr: ":7000"
http_addr: ":8000"
```

Run it:

```sh
lotund --config lotund.yaml
# lotund: control listening on :7000
# lotund: http listening on :8000
```

`token` and `base_domain` are required; `lotund` refuses to start without them.
Nothing here is production-shaped — the control port is plaintext and the token
is `dev`. Skip to [part 3](#3-use-the-client) to drive it, then come back.

### 2b. On a real server

The full walkthrough is the **[deployment guide](deploy.md)**. The shape of it:

1. **Wildcard DNS** — `*.yourdomain.com A <server-ip>`. Every tunnel is a
   subdomain, so one record covers all of them.
2. **Caddy in front** — `lotund` speaks plain HTTP on loopback; Caddy holds the
   wildcard certificate and terminates HTTPS on 443:

   ```caddyfile
   *.yourdomain.com {
   	reverse_proxy 127.0.0.1:8000
   }
   ```

   A wildcard certificate needs an ACME **DNS-01** challenge, so Caddy needs
   your DNS provider's plugin and credentials. See
   [deploy.md §2](deploy.md#2-caddy--wildcard-tls-in-front-of-the-http-port).
3. **Firewall** — open 443 and 80 (Caddy), the control port, and the TCP tunnel
   port range. Do **not** expose `http_addr`; Caddy reaches it on loopback.
4. **`lotund.yaml`** — a real one:

   ```yaml
   token: "a-long-random-secret"      # prefer the LOTUND_TOKEN env var
   base_domain: "yourdomain.com"
   control_addr: ":7000"
   control_tls_cert: "/etc/lotun/control.crt"
   control_tls_key: "/etc/lotun/control.key"
   http_addr: "127.0.0.1:8000"
   tcp_port_min: 20000
   tcp_port_max: 30000
   data_dir: "/var/lib/lotun"
   ```
5. **systemd** — see
   [deploy.md §5](deploy.md#5-run-it-as-a-service) for a unit that uses
   `DynamicUser` and `StateDirectory`.

> **Secure the control channel.** It carries the shared token and every
> tunneled byte, and it does *not* go through Caddy. Either set
> `control_tls_cert`/`control_tls_key` and have clients log in with `--tls`, or
> keep the port off the public internet entirely (WireGuard/Tailscale, an SSH
> tunnel, or a source-IP allowlist). Never expose it publicly in plaintext.
> Details: [deploy.md §Control-channel security](deploy.md#control-channel-security).

## 3. Use the client

### Log in

`lotun login` saves the server address and token to `~/.lotun/config.yaml` so
every later command picks them up. The file holds the token and any tunnel
passwords, so it is written `0600` inside a `0700` directory.

```sh
# local server from 2a
lotun login --server 127.0.0.1:7000 --token dev

# real server with control TLS
lotun login --server yourdomain.com:7000 --token "a-long-random-secret" --tls
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--server host:port` | Control address. Required. |
| `--token TOKEN` | Shared server token. Required. |
| `--tls` | Dial the control port over TLS. Certificate must be valid for the host in `--server`. |
| `--tls-insecure` | Skip certificate verification (self-signed control cert). Requires `--tls`. |
| `--default-domain name` | Subdomain to use when a command omits `--domain`. |

> `--tls-insecure` still encrypts, but accepts **any** certificate, so it does
> not stop an active man-in-the-middle. Lab use only.

Re-running `login` merges: it only overwrites the settings whose flags you
actually passed, and leaves a `tunnels:` list alone.

### Expose an HTTP port

```sh
python3 -m http.server 8080      # something to expose
lotun http 8080
# Tunnel ready: https://brave-otter.yourdomain.com -> localhost:8080
# Forwarding traffic. Press Ctrl-C to stop.
```

Without `--domain`, the server assigns a random memorable `adjective-animal`
name. The tunnel lives as long as the process.

Against the local server from 2a there is no Caddy and no DNS, so send the
`Host` header yourself:

```sh
curl -H 'Host: brave-otter.lvh.me' http://127.0.0.1:8000/
```

### Make it private (HTTP)

`--private` puts HTTP Basic Auth in front. The username is always `lotun`. With
no `--password`, the server generates one and the CLI prints it once:

```sh
lotun http 8080 --private
# Tunnel ready: https://brave-otter.yourdomain.com -> localhost:8080
# Generated password: 7f3c9a12b4e08d65

curl https://brave-otter.yourdomain.com/                             # 401
curl -u lotun:7f3c9a12b4e08d65 https://brave-otter.yourdomain.com/   # 200
```

Or set your own with `--password hunter2`.

### Expose a TCP port

TCP tunnels are routed by **port**, not by `Host`, and they do not pass through
Caddy — consumers connect straight to the server's IP on that port.

```sh
lotun tcp 25565
# Tunnel ready: brave-otter.yourdomain.com:25565 -> localhost:25565
```

The public port defaults to the local port, so a service keeps its natural port
(`lotun tcp 25565` is reachable on `:25565`). Override with `--remote-port`,
which must fall inside the server's `tcp_port_min`–`tcp_port_max` range and be
free:

```sh
lotun tcp 5432 --remote-port 25432
```

### Make it private (TCP)

Raw clients (a game client, `psql`) can't send a password, so `--private` on TCP
means a **source-IP allowlist**:

```sh
lotun tcp 5432 --remote-port 25432 --private --allow-ip 203.0.113.7 --allow-ip 198.51.100.4
```

`--allow-ip` repeats. `--private` on a TCP tunnel with no `--allow-ip` is an
error, and `--password` does not apply to TCP.

### Reserve a stable name

Random names change every run. Claim one to keep it, and to stop the random
assigner from handing it out:

```sh
lotun claim myapp
lotun http 8080 --domain myapp        # now stable at myapp.yourdomain.com
lotun unclaim myapp                   # release it
```

Claims are persisted in the server's `data_dir` and survive restarts. **An
explicit `--domain` must be claimed first** — registering an unclaimed name
fails with `registry: subdomain not claimed`.

### See what's running

```sh
lotun status
# SUBDOMAIN     TYPE   PUBLIC                              LOCAL PORT
# api           http   https://api.yourdomain.com          3000
# db            tcp    db.yourdomain.com:25432             5432
```

Lists the server's active tunnels and their public URLs. Prints
`No active tunnels.` when there are none.

### Run many tunnels, and keep them up

`lotun http` and `lotun tcp` are one-shot: one tunnel, and the process ends if
the connection drops. For a machine that should stay exposed, declare the whole
set in the client config and run `lotun serve` — one connection, all tunnels,
automatic reconnect with capped exponential backoff (1s doubling to 30s).

```yaml
# ~/.lotun/config.yaml — written by `lotun login`, tunnels added by hand
control_addr: yourdomain.com:7000
token: a-long-random-secret
tls: true

tunnels:
  - type: http
    domain: api          # claim it first: `lotun claim api`
    port: 3000
  - type: http
    domain: admin
    port: 4000
    private: true        # Basic Auth; omit `password` to have one generated
  - type: tcp
    domain: db
    port: 5432
    remote_port: 25432   # must be inside the server's tcp_port_min..max
    private: true
    allow_ips: ["203.0.113.7"]
```

```sh
lotun serve
# Tunnel ready: https://api.yourdomain.com -> localhost:3000
# Tunnel ready: https://admin.yourdomain.com -> localhost:4000
# Tunnel ready: db.yourdomain.com:25432 -> localhost:5432
# Forwarding traffic. Press Ctrl-C to stop.
```

Each tunnel needs its own subdomain, so `default_domain` is deliberately **not**
applied to `tunnels:` entries — give each one a `domain:` or omit it for a
server-assigned name.

To supervise it, use a systemd **user** unit — see
[deploy.md §6](deploy.md#6-run-the-client-as-a-service). There is no reload
signal: after editing `tunnels:`, restart the process.

## 4. Config reference

### Client — `~/.lotun/config.yaml`

Written by `lotun login`; `tunnels:` is added by hand. Override the path with
`--config`, which also gives you profiles (one file per environment).

| Key | Purpose |
| --- | --- |
| `control_addr` | `host:port` of the server's control listener. |
| `token` | Shared server token. |
| `tls` | Dial the control port over TLS. |
| `tls_insecure` | Skip certificate verification (needs `tls: true`). |
| `default_domain` | Subdomain used when `--domain` is omitted. Not applied to `tunnels:`. |
| `tunnels` | List served by `lotun serve` (see below). |

Per-entry keys under `tunnels:` mirror the flags: `type` (`http`/`tcp`),
`domain`, `port` (local), `remote_port` (tcp), `private`, `password` (http
only), `allow_ips` (tcp only).

### Server — `lotund.yaml`

Loaded with `--config`. `LOTUND_`-prefixed environment variables override file
values (`LOTUND_TOKEN` beats `token`), which is the right way to keep the token
off disk.

| Key | Default | Purpose |
| --- | --- | --- |
| `token` | *(required)* | Shared auth token; compared in constant time. |
| `base_domain` | *(required)* | Domain that tunnels are subdomains of. |
| `control_addr` | `:7000` | Control listener address. |
| `control_tls_cert` | `""` | Control TLS certificate; empty means plaintext. |
| `control_tls_key` | `""` | Control TLS key. |
| `http_addr` | `:8000` | Public HTTP listener; front it with Caddy, keep it on loopback. |
| `tcp_port_min` | `20000` | Lowest allocatable TCP tunnel port. |
| `tcp_port_max` | `30000` | Highest allocatable TCP tunnel port. |
| `data_dir` | `./data` | Where subdomain claims are persisted. |

## 5. Troubleshooting

| What you see | What it means |
| --- | --- |
| ``not configured: run `lotun login` first`` | No client config at `~/.lotun/config.yaml` (or the `--config` path). Log in. |
| `auth rejected: invalid token` | The client token doesn't match the server's. Check `LOTUND_TOKEN` isn't overriding the file on the server. |
| `tls: failed to verify certificate: x509: certificate signed by unknown authority` | `--tls` against a self-signed control cert. Install the CA in the client's trust store, or add `--tls-insecure` for a lab. |
| `--tls-insecure requires --tls` | `--tls-insecure` only relaxes an already-TLS dial; it does not enable TLS. |
| Client hangs, then a connection error on `login` | The control port isn't reachable — firewall, or you're dialing the plaintext port with `--tls` (or the reverse). |
| `register failed: registry: subdomain not claimed` | An explicit `--domain` must be claimed first: `lotun claim <name>`. |
| `register failed: registry: subdomain already has an http tunnel` | Another session already holds that subdomain — often an orphaned `lotun` still running. Stop it and retry. |
| `register failed: registry: requested port outside allowed range` | `--remote-port` is outside the server's `tcp_port_min`–`tcp_port_max`. |
| `register failed: registry: tcp port already in use` | Another tunnel holds that public port. Pick another `--remote-port`. |
| `a private tcp tunnel requires at least one allowed IP` | `--private` on TCP needs `--allow-ip` (repeatable). |
| `password is not valid for tcp` | TCP privacy is the IP allowlist, not Basic Auth. |
| `no tunnels configured` | `lotun serve` with no `tunnels:` list in the config. |
| HTTP 404 from the server | No tunnel registered for that subdomain, or Caddy is sending a `Host` that doesn't match `<name>.<base_domain>`. |
| HTTP 401 you didn't expect | The tunnel is `--private`. Use `-u lotun:<password>`. |
| TCP connects then drops instantly | The tunnel is `--private` and your source IP isn't on the allowlist. Check the address the server actually sees, not your LAN address. |
| Server exits at startup | `token` and `base_domain` are required. Check the `--config` path resolved to the file you meant. |

Logs are the fastest confirmation: `journalctl -u lotund -f` on the server
should show `control listening` and `http listening`, then a line per
registration.

## Where next

- [deploy.md](deploy.md) — the full production self-hosting guide.
- [protocol.md](protocol.md) — the wire format, if you're extending it.
- [DESIGN.md](DESIGN.md) — architecture and why it is shaped this way.
- [CONTRIBUTING.md](../CONTRIBUTING.md) — build, test, and commit conventions.
