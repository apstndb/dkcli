# Repository Instructions

## Build, test, and static checks

- Build: `go build ./...`
- Test: `go test ./...`
- Test with race detector: `go test ./... -race`
- Static checks: `go vet ./...`
- Single test: `go test ./cmd -run '^TestFetchBatchBisect$'`
- Single subtest: `go test ./cmd -run '^TestFormatDocWithFrontmatter$/^normal$'`

## High-level architecture

- `main.go` only calls `cmd.Execute()`.
- `cmd/root.go` centralizes shared HTTP behavior: global output flags, auth selection, rate limiting, retries, and structured output helpers.
- `search` targets the GA `v1` REST surface.
- `get` and `batch-get` target the GA `v1` REST surface.
- `answer-query` currently uses `v1alpha` because that is the documented live endpoint.
- `create-api-key` uses ADC against the API Keys API, then fetches the key string from the follow-up endpoint.
- Tests live beside the command code in `cmd/` and mostly use `httptest`; `newTestClient` in `cmd/batchget_test.go` is the shared fixture for document API tests.

## Key conventions

- For Developer Knowledge API commands (`search`, `get`, `batch-get`, and `answer-query`), `DEVELOPERKNOWLEDGE_API_KEY` or `GOOGLE_API_KEY` takes precedence; if neither is set, the CLI falls back to ADC.
- Local ADC calls need a quota project; the CLI resolves it from `GOOGLE_CLOUD_QUOTA_PROJECT` or `quota_project_id` in the ADC file and sends `x-goog-user-project`.
- Document names are normalized to `documents/<uri_without_scheme>`; raw paths and full `https://` URLs are both accepted.
- Search filtering is exposed as the API's AIP-160 `filter` string.
- Summary/status lines go to stderr; document and answer payloads go to stdout or the file selected with `--output`.
- Text output always ends with a trailing newline. Frontmatter output includes metadata fields like title, data source, update time, and view when available.
- User-facing behavior changes should keep `README.md` and `SKILL.md` aligned.
