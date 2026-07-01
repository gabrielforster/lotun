# Deploying lotun (production self-host)

This guide covers running `lotund` on a public server with your own domain:
wildcard DNS, TLS via Caddy, firewall rules, and the server config.

`lotund` is single-tenant. One shared token gates the server. It terminates
public TLS via a Caddy reverse proxy in front — `lotund` itself speaks plain
HTTP on localhost.

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

## 4. `lotund.yaml`

`lotund` loads a YAML config (via `-config`), layering `LOTUND_`-prefixed
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

# Control-channel TLS. Omit both for plaintext (dev/test only); set them for
# any real deploy so the token and tunnel traffic are encrypted in transit.
control_tls_cert: "/etc/lotun/control.crt"
control_tls_key: "/etc/lotun/control.key"

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
| `control_tls_cert` | `""` | Control-channel TLS cert. Empty → plaintext control. |
| `control_tls_key` | `""` | Control-channel TLS key. |
| `http_addr` | `:8000` | Public HTTP listener (front it with Caddy). |
| `tcp_port_min` | `20000` | Lowest allocatable TCP tunnel port. |
| `tcp_port_max` | `30000` | Highest allocatable TCP tunnel port. |
| `data_dir` | `./data` | Directory for persisted subdomain claims. |

`token` and `base_domain` are required — `lotund` refuses to start without them.

### Control-channel TLS certificate

The control channel is a plain TLS connection (not fronted by Caddy). Point
`control_tls_cert`/`control_tls_key` at a certificate valid for the host clients
dial. You can use a cert issued for `yourdomain.com`, or a self-signed cert (in
which case clients need to skip verification). Leaving both empty runs the
control channel in plaintext — acceptable only for local testing.

## 5. Run it

```sh
lotund -config /etc/lotun/lotund.yaml
```

Point clients at the control port:

```sh
lotun login --server yourdomain.com:7000 --token "a-long-random-secret"
lotun http 8080
```

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
