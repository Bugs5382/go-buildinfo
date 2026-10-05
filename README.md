# go-buildinfo 🐹

> 🧭 Build version and commit for Go services: ldflags stamping with a VCS fallback, reported on the gRPC health check and HTTP health endpoints.

One module, four packages:

| Package | What it does |
| --- | --- |
| `buildinfo` | `Version` and `Commit` stamped with `-ldflags -X`; `Get()` returns them with the Go version and the modified flag |
| `health` | Dependency checks with a timeout and a cached result, aggregated into readiness |
| `grpcbuildinfo` | Headers on `grpc.health.v1.Health/Check`, serving status driven by readiness, a client reader |
| `httpbuildinfo` | `/readyz`, `/livez` and the same headers over HTTP |

## 📦 Install

```bash
go get github.com/Bugs5382/go-buildinfo
```

## 🏷 Stamp the build

```bash
go build -ldflags "\
  -X github.com/Bugs5382/go-buildinfo.Version=$(git describe --tags --always) \
  -X github.com/Bugs5382/go-buildinfo.Commit=$(git rev-parse HEAD)" ./cmd/server
```

```go
info := buildinfo.Get() // {Version, Commit, GoVersion, Modified}
```

An unstamped version is `dev`. An unstamped commit falls back to the `vcs.revision` Go records
when it builds inside a git checkout, then to `unknown`. (The go command only records VCS info
when `.git` is a directory, so a linked `git worktree` builds without it.)

## 🩺 Dependencies and readiness

```go
checker := health.New(
	health.WithTTL(5*time.Second),     // reuse a result this long
	health.WithTimeout(2*time.Second), // bound every check
	health.WithLogger(logger),         // log state changes (go-log)
)
err := checker.Register(
	health.Dependency{Name: "postgres", Required: true, Check: pool.Ping, Version: pgVersion(pool)},
	health.Dependency{Name: "cache", Check: valkeyPing(client)}, // optional: degraded, still ready
)
```

- A **required** dependency that is down makes the service not ready: gRPC `NOT_SERVING`, HTTP 503.
  An **optional** one reports `degraded` and the service stays ready.
- Readiness recovers on its own when the dependency comes back. Liveness never checks
  dependencies, so an outage of a database cannot get every replica restarted.
- A slow, failing or panicking check marks its dependency down; it never fails the call. A check
  that never returns is not started again until it does.
- Reports carry an error class (`timeout`, `connection-refused`, `dns`, `network`, `canceled`,
  `panic`, `error`, or your own with `health.Classify`), never the raw error. Version strings
  that look like a URL, a `key=value` pair or anything outside a version's characters are sent as
  `redacted`.

### Short checkers

`health.CheckHTTP(client, url)` and `health.CheckTCP(addr)` ship with the package. For the rest,
use the ping your client library already has:

```go
// PostgreSQL (pgx)
Check:   pool.Ping,
Version: func(ctx context.Context) (string, error) {
	var v string
	return v, pool.QueryRow(ctx, "SHOW server_version").Scan(&v)
},

// Valkey / Redis (go-redis or valkey-go)
Check: func(ctx context.Context) error { return rdb.Ping(ctx).Err() },

// RabbitMQ (amqp091-go): a closed connection is down
Check: func(context.Context) error {
	if conn.IsClosed() {
		return errors.New("connection closed")
	}
	return nil
},
Version: func(context.Context) (string, error) {
	v, _ := conn.Properties["version"].(string)
	return v, nil
},

// OPA, an identity server, anything with an HTTP health endpoint
Check: health.CheckHTTP(httpClient, "http://opa:8181/health"),
```

## 📡 gRPC

```go
hs := grpchealth.NewServer()
bi, err := grpcbuildinfo.New(
	grpcbuildinfo.WithPrefix("acme"),            // acme-version, acme-commit, ...
	grpcbuildinfo.WithChecker(checker),
	grpcbuildinfo.WithHealthServer(hs),
	grpcbuildinfo.WithServices("acme.v1.Things"), // follow readiness too
	grpcbuildinfo.WithLivenessService("liveness"),
)
srv := grpc.NewServer(
	grpc.ChainUnaryInterceptor(bi.UnaryServerInterceptor()),
	grpc.ChainStreamInterceptor(bi.StreamServerInterceptor()),
)
healthpb.RegisterHealthServer(srv, hs)
go bi.Run(ctx) // keeps Health/Watch subscribers current
```

`Health/Check` answers carry:

| Header | Value |
| --- | --- |
| `<prefix>-version` | the build's version |
| `<prefix>-commit` | the build's commit |
| `<prefix>-dep-<name>` | a dependency's version (when it has a `Version` func) |
| `<prefix>-depstate-<name>` | `ok`, `degraded` or `down` |

The prefix defaults to `app`. It must be lower-case letters, digits and single dashes, start with
a letter, and must not start with `grpc`. `WithAllRPCs()` adds the version and commit to every RPC.

Read another service's answer:

```go
res, err := grpcbuildinfo.Read(ctx, conn, "acme")
// res.Status, res.Version, res.Commit, res.Dependencies["postgres"].State
```

## 🌐 HTTP

```go
h, err := httpbuildinfo.New(httpbuildinfo.WithPrefix("acme"), httpbuildinfo.WithChecker(checker))
mux.Handle("/readyz", h.Readyz())
mux.Handle("/livez", h.Livez())
mux.Handle("/status", h.Middleware(statusHandler))
```

`/readyz` returns 200 or 503 with:

```json
{
  "status": "degraded",
  "ready": true,
  "build": {"version": "v1.2.3", "commit": "abc123", "goVersion": "go1.26.3", "modified": false},
  "dependencies": [
    {"name": "postgres", "state": "ok", "required": true, "checkedAt": "2030-01-02T03:04:05Z", "version": "17.2"},
    {"name": "cache", "state": "degraded", "required": false, "error": "timeout", "checkedAt": "2030-01-02T03:04:05Z"}
  ]
}
```

`/livez` returns 200 `{"status":"ok"}` and never checks dependencies.

## 🛠 Develop

```bash
task build             # go build ./...
task test              # go test ./...
task test-integration  # real PostgreSQL, RabbitMQ and Valkey containers (needs Docker)
task lint              # gofmt check + golangci-lint + yamllint
task license           # check MIT headers (golic)
```

## ⚖️ License

MIT (c) 2026 Shane
