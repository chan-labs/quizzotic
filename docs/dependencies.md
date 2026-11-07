# Dependencies

Quizzotic is stdlib-first. Only the third-party modules below are allowed (see "Architecture" in [spec.md](spec.md)). JWT verification, JWKS fetching, PKCE, sessions and routing are written on the standard library.

Adding any other dependency needs the maintainer's approval and a new entry here, stating what it is for and why the standard library is not enough.

| Module | Why it is allowed |
| --- | --- |
| `modernc.org/sqlite` | Pure-Go SQLite driver; keeps the binary cgo-free. |
| `github.com/jackc/pgx/v5` | Postgres driver. |
| `gopkg.in/yaml.v3` | Config file, OpenAPI spec and seed file parsing. |
| `github.com/modelcontextprotocol/go-sdk` | Official MCP protocol implementation (stdio transport, tool registry, test client); avoids protocol drift. Its own transitive requirements (OAuth2, JWT, JSON schema) come with it and are not used directly by Quizzotic code. |
| `github.com/yuin/goldmark` | Markdown rendering for prompts and explanations, with raw HTML disabled. |
| `go.opentelemetry.io/otel` (+ SDK and `contrib/exporters/autoexport`) | Tracing that honors the standard `OTEL_*` environment variables. |

Build tooling (GoReleaser, golangci-lint) is run via `go run` or installed by mise at pinned versions and is not part of `go.mod`, so it is outside the runtime-dependency rule above.
