# Contributing to lotun

## Prerequisites

Go 1.25.5 or newer (see `go.mod`). Nothing else — all dependencies are pinned
in `go.mod`/`go.sum` and the test suite is fully in-process (no Docker, no real
DNS, no network).

## Build and run

```sh
go build ./...                       # everything
go build -o lotun  ./cmd/lotun       # client CLI
go build -o lotund ./cmd/lotund      # server daemon
```

To run the pair locally against `lvh.me` (which resolves `*.lvh.me` to
`127.0.0.1`), follow [docs/guide.md](docs/guide.md) — it is also the manual
smoke test.

The client CLI version is injected at build time:

```sh
go build -ldflags "-X main.version=$(git describe --tags --always)" ./cmd/lotun
```

## The gate

Every change must pass all three before it is merged:

```sh
gofmt -l .            # prints nothing
go vet ./...          # clean
go test -race ./...   # green
```

`-race` is not optional: the server multiplexes concurrent streams and the
registry is shared mutable state, so data races are the failure mode that
matters here.

## Testing approach

Development is test-driven: write the failing test, make it pass, refactor.

Tests are in-process by design — yamux runs over any `net.Conn`, so the control
handshake is tested over `net.Pipe()` and the end-to-end tests wire a real
server and a real client over loopback with `:0` listeners. If you find yourself
reaching for a sleep, a fixed port, or a real domain, there is usually a seam
you can use instead (see `connectOverConn` in `internal/client`).

`internal/e2e` holds the full-path tests: an HTTP tunnel through the vhost
router, a TCP tunnel through an allocated public port, private variants of both,
multiple tunnels on one session, and the TCP half-close regression test.

## Layout

| Path | Responsibility |
| --- | --- |
| `cmd/lotun` | Client CLI (cobra commands, flag validation, `serve` loop). |
| `cmd/lotund` | Server daemon entrypoint. |
| `internal/protocol` | Wire format: message types, length-prefixed JSON framing, stream header. |
| `internal/client` | Dial, auth, register, accept data streams, forward to localhost. |
| `internal/server` | `server.go` wiring, `registry.go` state, `http.go` vhost router, `tcp.go` per-tunnel listeners. |
| `internal/config` | Viper-backed client and server config. |
| `internal/store` | Subdomain claim persistence (JSON file). |
| `internal/names` | Random `adjective-animal` name generator. |
| `internal/netutil` | `Splice`: half-close-aware bidirectional copy, shared by client and server. |
| `internal/e2e` | In-process end-to-end tests. |

`internal/client` and `internal/server` must not import each other — anything
they both need goes in a shared package (that is why `internal/netutil` exists).

Every exported symbol carries a doc comment, and every package has a package
doc. Keep it that way; `go vet` will not catch it for you.

Deliberate simplifications with a known ceiling are marked with a `ponytail:`
comment naming the ceiling and the upgrade path, e.g.:

```go
// ponytail: JSON file store — single-tenant, low write volume. Swap for SQLite when multi-tenant.
```

## Commits

[Conventional Commits](https://www.conventionalcommits.org/), imperative mood,
lowercase subject:

```
<type>(<scope>): <subject>
```

- Types: `feat`, `fix`, `test`, `refactor`, `docs`, `chore`.
- Scopes: `protocol`, `names`, `store`, `config`, `registry`, `client`,
  `server`, `cli`, `e2e`, `docs`.
- Keep commits small — typically a `test(...)` commit followed by a `feat(...)`
  commit, or one `feat(...)` per behavior.
- **No `Co-Authored-By` trailers.** No AI or co-author attribution in any commit.

Examples:

```
test(protocol): add round-trip encode/decode cases
feat(registry): reject duplicate (subdomain,port) registration
fix(server): close yamux stream on upstream dial failure
docs(readme): document self-host with Caddy
```

## Dependencies

Do not run `go mod tidy` casually — the deps are pinned deliberately. If you
genuinely need a new one, `go get <pkg>@<version>` and call it out in the pull
request. Stdlib first: this project reaches for `net`, `net/http`,
`net/http/httputil`, `crypto/tls`, and `crypto/subtle` before adding anything.

## License

By contributing you agree that your contribution is licensed under the
[GNU AGPL v3.0 or later](LICENSE), the same terms as the rest of the project.

## Documentation

Behavior changes that alter the wire format, the CLI surface, or how the thing
is deployed need the matching doc updated in the same change:

- [README.md](README.md) — pitch, quick start, CLI reference.
- [docs/guide.md](docs/guide.md) — install, host, use.
- [docs/deploy.md](docs/deploy.md) — production self-hosting.
- [docs/protocol.md](docs/protocol.md) — wire format.
- [docs/DESIGN.md](docs/DESIGN.md) — architecture and decisions.
