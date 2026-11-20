# go-devin-client

This repository generates a Go client for Devin's published v3 API. `v3-openapi.yaml` is the byte-identical source; run `script/generate` to regenerate the typed client and `script/check-generated` to verify it.

Use `New(Config)` for authenticated API access. `NewClient(baseURL)` is the lower-level unauthenticated generated constructor. The four operations omitted from generated methods have handwritten SDK implementations; see [the operation inventory](codegen/EXCLUDED_OPERATIONS.md).

## CLI

Install the `devin-client` wrapper with `go install github.com/shakefu/go-devin-client/cmd/devin-client@latest`. It exposes the full Devin API v3 operation set; v1 and v2 compatibility are not supported. `devin-client ops` lists operations and `devin-client <operationId> --help` shows an operation's generated parameter flags.

Settings are selected in flag, environment, default order. `--api-key` / `DEVIN_API_KEY` supplies the credential; `--base-url` / `DEVIN_BASE_URL` defaults to `https://api.devin.ai`; `--timeout` / `DEVIN_TIMEOUT` accepts a Go duration and defaults to 60 seconds. `--allow-insecure-http` is required for an HTTP test server. `--read-only` rejects writes before reading inputs, and `--dry-run` prints a redacted request without credentials or network access.

Maintainer bootstrap requires `BOT_CLIENT_ID` and `BOT_PRIVATE_KEY`; do not store their values in the repository.

```sh
go install github.com/shakefu/go-devin-client/cmd/devin-client@latest
export DEVIN_API_KEY=...
devin-client ops
devin-client handle_get_account_sessions_insights_v3_enterprise_sessions_insights_get
devin-client create_enterprise_blueprint_endpoint_v3_enterprise_snapshot_setup_blueprints_post --data '{"name":"example"}' --dry-run
```

Successful API response bytes are written unchanged to stdout. The CLI uses the authenticated SDK transport, including for its handwritten multipart and self-endpoint helpers; it does not retry, paginate, or validate domain request bodies locally.
