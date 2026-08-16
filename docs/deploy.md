# Deploying lotun (production self-host)

This guide covers running `lotund` on a public server with your own domain:
wildcard DNS, TLS via Caddy, firewall rules, and the server config.

`lotund` is single-tenant. One shared token gates the server. It terminates
public TLS via a Caddy reverse proxy in front — `lotund` itself speaks plain
HTTP on localhost.

For the client side — installing, logging in, and exposing ports — see
[guide.md](guide.md). This document is the server half.

## What gets exposed

`lotund` opens three network surfaces; only the HTTP one goes through Caddy:

| Surface | Config | Who connects | TLS |
| --- | --- | --- | --- |
| HTTP tunnels | `http_addr` (keep on loopback) | Caddy, which reverse-proxies to it | Caddy terminates wildcard HTTPS on 443 |
| Control channel | `control_addr` (e.g. `:7000`) | `lotun` clients, directly | see [Control-channel security](#control-channel-security) |
| TCP tunnels | ports in `tcp_port_min`–`tcp_port_max` | consumers, directly on the public IP | none (raw TCP) |

HTTP tunnels are published as `https://<name>.<base_domain>` and routed by the
`Host` header; TCP tunnels are reached at `<name>.<base_domain>:<port>` and
routed purely by port. Work through the sections below in order.

## 1. Wildcard DNS

Every tunnel is addressed by a subdomain of your base domain, so point a
wildcard record at your server's IP:

```
*.yourdomain.com.   A     203.0.113.10
yourdomain.com.     A     203.0.113.10   # optional, for the apex
```

Both tunnel types use the wildcard record. HTTP tunnels are routed by the `Host`
header on the shared HTTP port; TCP tunnels are routed by port (DNS just
resolves the name to the server IP — see the note at the end).

## 2. Caddy — wildcard TLS in front of the HTTP port

`lotund`'s `http_addr` defaults to `:8000` and serves plain HTTP. Put Caddy in
front to obtain and renew a wildcard certificate and reverse-proxy to it:

```caddyfile
*.yourdomain.com {
	reverse_proxy 127.0.0.1:8000
}
```

Caddy needs a **DNS-01 challenge** to issue a wildcard certificate
(`*.yourdomain.com`), which requires a Caddy build with your DNS provider's
plugin and the provider credentials configured. See the Caddy docs for the
`tls` / `acme_dns` directive for your provider. Once that's set, Caddy handles
HTTPS and HTTP/2 on 443, and `lotund` only ever sees plain HTTP on 127.0.0.1.

The port in `reverse_proxy 127.0.0.1:<port>` must match the port in your
`http_addr` (default `8000`).

## 3. Firewall

Open exactly what's needed:

- **443** (and 80 for ACME/redirects) — Caddy's public HTTPS.
- **The control port** — where `lotun` clients connect (default `7000`).
- **The TCP tunnel port range** — the ports `lotund` allocates for TCP tunnels.
  This must match `tcp_port_min`–`tcp_port_max` in the config (default
  `20000`–`30000`).

Example with `ufw`:

```sh
ufw allow 443/tcp
ufw allow 80/tcp
ufw allow 7000/tcp
ufw allow 20000:30000/tcp
```

Do **not** expose `http_addr` (`:8000`) publicly — Caddy reaches it on
loopback. Bind it to `127.0.0.1` if your host is multi-homed.

If you follow the private-network or SSH-tunnel option in
[Control-channel security](#control-channel-security), **drop** the `7000` rule
and keep the control port off the public internet entirely.

## 4. `lotund.yaml`

`lotund` loads a YAML config (via `--config`), layering `LOTUND_`-prefixed
environment variables and built-in defaults over it (e.g. `LOTUND_TOKEN`
overrides `token`). A production config:

```yaml
# Shared auth token. Every client must present this exact value.
# Prefer supplying it via the LOTUND_TOKEN env var rather than on disk.
token: "a-long-random-secret"

# Base domain; tunnels become <name>.<base_domain>.
base_domain: "yourdomain.com"

# Control listener: where lotun clients connect.
control_addr: ":7000"

# Control-channel TLS. Set these when the control port is reachable from the
# public internet; clients then log in with `--tls`. See "Control-channel
# security" below.
# control_tls_cert: "/etc/lotun/control.crt"
# control_tls_key: "/etc/lotun/control.key"

# Public HTTP listener (Caddy reverse-proxies to this). Keep it on loopback.
http_addr: "127.0.0.1:8000"

# Allocatable public port range for TCP tunnels (inclusive).
tcp_port_min: 20000
tcp_port_max: 30000

# Where subdomain claims are persisted (a JSON file lives here).
data_dir: "/var/lib/lotun"
```

Config fields (all keys are snake_case; env overrides use the `LOTUND_` prefix):

| Key | Default | Purpose |
| --- | --- | --- |
| `token` | *(required)* | Shared auth token; compared in constant time. |
| `base_domain` | *(required)* | Domain that tunnels are subdomains of. |
| `control_addr` | `:7000` | Control listener address. |
| `control_tls_cert` | `""` | Control-channel TLS cert; empty means plaintext. Clients dial it with `lotun login --tls` (see [Control-channel security](#control-channel-security)). |
| `control_tls_key` | `""` | Control-channel TLS key. |
| `http_addr` | `:8000` | Public HTTP listener (front it with Caddy). |
| `tcp_port_min` | `20000` | Lowest allocatable TCP tunnel port. |
| `tcp_port_max` | `30000` | Highest allocatable TCP tunnel port. |
| `data_dir` | `./data` | Directory for persisted subdomain claims. |

`token` and `base_domain` are required — `lotund` refuses to start without them.

### Control-channel security

The control channel is a direct connection between `lotun` clients and
`control_addr` — it does **not** go through Caddy. Everything rides it: the auth
token, every registration, and all tunneled bytes. If the control port is
reachable from the public internet, encrypt it.

**Option A — control TLS (recommended when the port is public).** Point
`control_tls_cert`/`control_tls_key` at a certificate for the host clients dial,
then have clients pass `--tls` at login:

```sh
lotun login --server yourdomain.com:7000 --token "a-long-random-secret" --tls
```

The certificate must be valid for the name in `--server`. A public certificate
is the simplest path — reuse the one Caddy already obtains, or issue a
dedicated one. For a self-signed or internal-CA certificate, either install the
CA in the client's trust store or add `--tls-insecure`, which skips verification
entirely:

```sh
lotun login --server yourdomain.com:7000 --token "..." --tls --tls-insecure
```

> `--tls-insecure` still encrypts the connection but accepts **any**
> certificate, so it does not protect against an active man-in-the-middle. Use
> it for a lab or a first run, not as a permanent setting.

**Option B — keep the port off the public internet.** Just as good, and it
avoids certificate management:

- **Private network:** run the control port on a WireGuard or Tailscale
  interface and have clients dial that address. The token and all tunneled
  bytes then ride an already-encrypted link, and you don't open `7000` to the
  public internet at all.
- **SSH tunnel:** `ssh -L 7000:127.0.0.1:7000 you@yourdomain.com`, keep
  `control_addr` on `127.0.0.1:7000`, and `lotun login --server 127.0.0.1:7000`.
- **Source-IP allowlist:** if your clients have stable IPs, restrict the control
  port in the firewall to just those addresses.

What you must not do is expose `control_addr` publicly in plaintext — the shared
token crosses it on every connection.

## 5. Run it as a service

Build and install the binary:

```sh
go build -o /usr/local/bin/lotund ./cmd/lotund
```

Run it under systemd — `/etc/systemd/system/lotund.service`:

```ini
[Unit]
Description=lotun tunnel server
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/lotund --config /etc/lotun/lotund.yaml
Restart=on-failure
DynamicUser=yes
StateDirectory=lotun            # provisions /var/lib/lotun (must match data_dir)
WorkingDirectory=/var/lib/lotun

[Install]
WantedBy=multi-user.target
```

All `lotund` ports are above 1024, so no `CAP_NET_BIND_SERVICE` is needed.
`StateDirectory=lotun` creates `/var/lib/lotun` owned by the dynamic user — keep
`data_dir` pointed there. Enable and inspect it:

```sh
systemctl daemon-reload && systemctl enable --now lotund
journalctl -u lotund -f        # expect "control listening" and "http listening"
```

Then point clients at the control port:

```sh
lotun login --server yourdomain.com:7000 --token "a-long-random-secret" --tls
lotun http 8080
```

## 6. Run the client as a service

`lotun http`/`lotun tcp` are one-shot foreground commands: one tunnel, and the
process ends if the control connection drops. For a machine that should stay
exposed, declare the tunnels in the client config and run `lotun serve`, which
registers them all on a single control session and reconnects on failure.

```yaml
# ~/.lotun/config.yaml — written by `lotun login`, tunnels added by hand
control_addr: yourdomain.com:7000
token: a-long-random-secret
tls: true

tunnels:
  - type: http
    domain: api          # must be claimed first: `lotun claim api`
    port: 3000
  - type: http
    domain: admin
    port: 4000
    private: true        # Basic Auth; omit `password` to have one generated
  - type: tcp
    domain: db
    port: 5432
    remote_port: 25432   # must fall inside the server's tcp_port_min..max
    private: true
    allow_ips: ["203.0.113.7"]
```

The file holds the token and any tunnel passwords, so `lotun login` writes it
`0600` inside a `0700` directory. Re-running `lotun login` keeps the `tunnels:`
list intact.

Supervise it with a systemd **user** unit —
`~/.config/systemd/user/lotun.service`:

```ini
[Unit]
Description=lotun tunnel client
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/lotun serve
Restart=on-failure

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload && systemctl --user enable --now lotun
systemctl --user status lotun
journalctl --user -u lotun -f     # one "Tunnel ready" line per tunnel
loginctl enable-linger "$USER"    # keep it running when you are not logged in
```

`lotun serve` reconnects on its own with capped exponential backoff (1s doubling
to 30s), so a `lotund` restart or a flaky link recovers without systemd getting
involved. There is no reload signal: after editing `tunnels:`, restart the unit
(`systemctl --user restart lotun`).

Each tunnel needs its own subdomain, so `default_domain` is not applied to
`tunnels:` entries — name each one with `domain:` (claim it first) or omit
`domain:` to let the server assign a random name.

## Note: raw TCP tunnels bypass Caddy

TCP tunnels are **not** proxied through Caddy. When a client runs `lotun tcp
9000`, `lotund` opens a public listener on the allocated port and consumers
connect **directly** to:

```
name.yourdomain.com:<port>
```

DNS resolves `name.yourdomain.com` to the server IP; the **port** is what
selects the tunnel on the server. So a TCP tunnel needs its port open in the
firewall (within `tcp_port_min`–`tcp_port_max`) — Caddy and TLS are not in the
path. If you want TLS on a TCP service, terminate it inside your own service.
