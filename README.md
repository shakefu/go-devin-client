# go-devin-client

This repository generates a Go client for Devin's published v3 API. `v3-openapi.yaml` is the byte-identical source; run `script/generate` to regenerate the typed client and `script/check-generated` to verify it.

The generated `NewClient(baseURL)` constructor is lower-level and unauthenticated. The three multipart operations and `GET /v3/self` remain explicit follow-up SDK work; see [the exclusion inventory](codegen/EXCLUDED_OPERATIONS.md).
