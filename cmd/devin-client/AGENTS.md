# Devin CLI

- `main.go` forwards signals and standard streams to `internal/devincli.Run`.
- The CLI projection is generated from the repository's `v3-openapi.yaml`:
  run `go generate ./cmd/devin-client/internal/devincli` from the repository root.
- `internal/devincli` separates projection/commands, settings, request building,
  output, and the pending SDK dispatcher boundary. Keep CLI tests on local
  `httptest` servers; production calls await the merged authenticated SDK.
