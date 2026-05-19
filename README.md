# dkcli

CLI client for the [Google Developer Knowledge API](https://developerknowledge.googleapis.com/).

## Install

```
go install github.com/apstndb/dkcli@latest
```

## Authentication

Developer Knowledge API commands use Application Default Credentials (ADC) by default, except `create-api-key` which always requires ADC.

Set up ADC:

```
gcloud auth application-default login
```

When using local ADC, the Developer Knowledge API also requires a quota project. dkcli reads that from `GOOGLE_CLOUD_QUOTA_PROJECT` or `quota_project_id` in the ADC file. The standard way to set that up is `gcloud auth application-default set-quota-project <project-id>`.

If you prefer to use an API key instead of ADC, set one of the following environment variables:

```
export DEVELOPERKNOWLEDGE_API_KEY=<your-key>
# or
export GOOGLE_API_KEY=<your-key>
```

## Usage

### Search documents

```
dkcli search "how to create a Cloud Storage bucket"

# Auto-paging fetches subsequent pages automatically (default max: 5 pages)
dkcli search -a "Cloud Storage"

# Restrict results to a specific corpus source
dkcli search --filter 'dataSource = "docs.cloud.google.com"' "BigQuery"

# Fetch more pages
dkcli search -a --max-pages 20 "Cloud Storage"
```

### Get a document

```
dkcli get docs.cloud.google.com/storage/docs/creating-buckets
# Full URL also works
dkcli get https://docs.cloud.google.com/storage/docs/creating-buckets
```

If you already know the document URL, skip `search` and go straight to `get` or `batch-get`.

### Get multiple documents

```
dkcli batch-get docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2

# Write each document as a separate file
dkcli batch-get --outdir out docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2

# With YAML frontmatter
dkcli batch-get --outdir out --frontmatter docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2

```

### Create an API key

Creates an API key restricted to the Developer Knowledge API.

```
dkcli create-api-key --project my-gcp-project
# or
export GOOGLE_CLOUD_PROJECT=my-gcp-project
dkcli create-api-key
```

### Answer a grounded query

Uses the `v1alpha:answerQuery` endpoint. It works with an API key, or with ADC if a quota project is available.

```
dkcli answer-query "How do I create a Cloud Storage bucket?"
```

**Note:** This endpoint returns generated text without source URLs or grounding chunks. For verifiable information, prefer the `search` + `get` workflow.

## Global flags

| Flag | Short | Description |
|------|-------|-------------|
| `--format` | `-f` | Output format: `text` (default), `json`, `jsonl`, `yaml`, `txtar` |
| `--output` | `-o` | Write output to file instead of stdout |
| `--verbose` | `-v` | Dump response headers to stderr |

## Command-specific flags

### `search`

| Flag | Description |
|------|-------------|
| `--page-size` | Max results to return (max: 20, default is set by the API) |
| `--page-token` | Pagination token from previous response |
| `--auto-paging`, `-a` | Automatically fetch subsequent pages |
| `--max-pages` | Max pages to fetch with `--auto-paging` (default: 5, 0 for unlimited) |
| `--filter` | AIP-160 filter on document metadata |

### `get`

| Flag | Description |
|------|-------------|
| `--frontmatter` | Prepend YAML frontmatter to content (`--format=text` only; incompatible with `--size-only`) |
| `--size-only` | Print document size only, suppress content |

### `batch-get`

| Flag | Description |
|------|-------------|
| `--outdir` | Write each document to a separate file under this directory |
| `--frontmatter` | Prepend YAML frontmatter to content (text format only) |
| `--size-only` | Print document sizes only, suppress content |

### `create-api-key`

| Flag | Short | Description |
|------|-------|-------------|
| `--project` | `-p` | GCP project ID (env: `GOOGLE_CLOUD_PROJECT`) |
| `--display-name` | `-d` | Display name for the key (default: `dkcli`) |
| `--key-only` |  | Print only the API key string (for use in scripts) |

## Rate limiting

The Developer Knowledge API has a 100 RPM quota. dkcli includes a built-in rate limiter (burst 5, then ~1.67 req/s) and automatic retry with exponential backoff on HTTP 429 responses.
