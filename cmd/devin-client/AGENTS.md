# Devin CLI

- `main.go` forwards signals and standard streams to `internal/devincli.Run`, and exits promptly when signal context is canceled.
- The CLI projection is generated from the repository's `v3-openapi.yaml`:
  run `go generate ./cmd/devin-client/internal/devincli` from the repository root.
- `internal/devincli` separates projection/commands, settings, request building,
  output, and the authenticated SDK dispatcher. Nullable schemas use their
  non-null type; object, union, and unsupported parameter types normalize to
  string flags only where okapi cannot represent their source type.
  Keep CLI tests on local `httptest` servers.
