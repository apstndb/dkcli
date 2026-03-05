# dkcli

CLI client for the [Google Developer Knowledge API](https://developerknowledge.googleapis.com/).

## Install

```
go install github.com/apstndb/dkcli@latest
```

## Authentication

Most commands use an API key. Set one of the following environment variables:

```
export DEVELOPERKNOWLEDGE_API_KEY=<your-key>
# or
export GOOGLE_API_KEY=<your-key>
```

The `create-api-key` command uses Application Default Credentials instead:

```
gcloud auth application-default login
```

## Usage

### Search documents

```
dkcli search "how to create a Cloud Storage bucket"

# Auto-paging fetches subsequent pages automatically (default max: 5 pages)
dkcli search -a "Cloud Storage"

# Fetch more pages
dkcli search -a --max-pages 20 "Cloud Storage"
```

### Get a document

```
dkcli get docs.cloud.google.com/storage/docs/creating-buckets
# Full URL also works
dkcli get https://docs.cloud.google.com/storage/docs/creating-buckets
```

### Get multiple documents

```
dkcli batch-get docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2

# Write each document as a separate file
dkcli batch-get --outdir out docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2

# With YAML frontmatter
dkcli batch-get --outdir out --frontmatter docs.cloud.google.com/path/to/doc1
```

### Create an API key

Creates an API key restricted to the Developer Knowledge API.

```
dkcli create-api-key --project my-gcp-project
# or
export GOOGLE_CLOUD_PROJECT=my-gcp-project
dkcli create-api-key
```

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
| `--page-size` | Max results to return (default: 5, max: 20) |
| `--page-token` | Pagination token from previous response |
| `--auto-paging`, `-a` | Automatically fetch subsequent pages |
| `--max-pages` | Max pages to fetch with `--auto-paging` (default: 5, 0 for unlimited) |

### `get`

| Flag | Description |
|------|-------------|
| `--frontmatter` | Prepend YAML frontmatter to content |

### `batch-get`

| Flag | Description |
|------|-------------|
| `--outdir` | Write each document to a separate file under this directory |
| `--frontmatter` | Prepend YAML frontmatter to content (text format only) |

### `create-api-key`

| Flag | Short | Description |
|------|-------|-------------|
| `--project` | `-p` | GCP project ID (env: `GOOGLE_CLOUD_PROJECT`) |
| `--display-name` | `-d` | Display name for the key (default: `dkcli`) |

## Rate limiting

The Developer Knowledge API has a 100 RPM quota (Preview). dkcli includes a built-in rate limiter (burst 5, then ~1.67 req/s) and automatic retry with exponential backoff on HTTP 429 responses.
