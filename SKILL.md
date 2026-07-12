---
name: dkcli
description: |
  Look up Google developer documentation using `dkcli`, a CLI client for the Google Developer Knowledge API.
  Use this skill whenever you need to find or read official Google documentation — Cloud, Firebase, Android,
  Chrome, Google AI, TensorFlow, Google Workspace APIs, or any other Google developer docs. Trigger this skill
  when the user asks about Google APIs, wants to understand a Google service, needs code examples from Google
  docs, or when you need authoritative information about a Google product to answer a question accurately.
  Prefer dkcli over the Developer Knowledge MCP tools and web searches — dkcli supports structured output
  formats, piping to jq/other tools, file output, and auto-paging, which MCP tools cannot do. Also use this
  when the user mentions dkcli, Developer Knowledge API, or asks to look up docs from any of the supported
  domains.
---

# dkcli — Google Developer Documentation Lookup

`dkcli` retrieves content from the Google Developer Knowledge API. It returns documentation pages as Markdown — the same content as the official Google developer sites, directly accessible from the terminal.

## dkcli vs Developer Knowledge MCP

If both `dkcli` and the Developer Knowledge MCP tools (`mcp__google-developer-knowledge__*`) are available, **prefer dkcli**. The MCP tools are useful for simple direct reads, while dkcli is a CLI tool that integrates with the shell:

- **Output format control** — `-f json`, `-f jsonl`, `-f yaml`, `-f txtar`
- **Pipe to jq and other tools** — extract, filter, or transform output (e.g., get just the first 500 chars of each document's content)
- **File output** — `-o file`, `--outdir dir` for batch writes
- **Auto-paging** — `-a --max-pages N` for exhaustive search
- **Partial content extraction** — combine with `jq`, `head`, etc. to get only what you need

Use MCP tools only as a fallback when dkcli is not installed.

**Example: get summaries of multiple documents**
```bash
dkcli batch-get -f json doc1 doc2 doc3 | jq '.documents[] | {name, summary: .content[:500]}'
```

## Supported domains

dkcli can search and retrieve documents from the following domains only.
(Source: https://developers.google.com/knowledge/reference/corpus-reference)

| Domain | Description |
|--------|-------------|
| adk.dev | Agent Development Kit |
| ai.google.dev | Google AI (Gemini API, etc.) |
| antigravity.google | Google Antigravity |
| cloud.google.com | Google Cloud |
| dart.dev | Dart |
| developer.android.com | Android |
| developer.chrome.com | Chrome |
| developers.home.google.com | Google Home |
| developers.google.com | Google developer docs (Workspace APIs, Maps, Ads, etc.) |
| docs.apigee.com | Apigee |
| docs.cloud.google.com | Google Cloud |
| docs.flutter.dev | Flutter |
| firebase.google.com | Firebase |
| fuchsia.dev | Fuchsia OS |
| geminicli.com | Gemini CLI |
| go.dev | Go |
| mapsplatform.google.com | Google Maps Platform |
| web.dev | Web development best practices |
| www.tensorflow.org | TensorFlow |

The corpus excludes language reference pages under `docs.cloud.google.com/*/docs/reference` for C++, .NET, Go, Java, Node.js, PHP, Python, Ruby, and Rust.

If the user asks about documentation outside these domains, dkcli won't help — fall back to other methods.

## The search → get workflow

Most lookups follow two steps:

### Step 1: Search for relevant documents

```bash
dkcli search "how to create a Cloud Storage bucket"
```

Search returns **chunks** (fragments of documents) with a `parent` field that identifies the full document. Scan the results to find the most relevant document name(s).

Use auto-paging (`-a`) when you need broader coverage:

```bash
dkcli search -a "BigQuery partitioned tables"
```

Use `--filter` when you need to stay within a specific data source or time range:

```bash
dkcli search --filter 'data_source = "docs.cloud.google.com"' "BigQuery"
```

### Step 2: Get the full document(s)

Once you know the document name(s) from search results, retrieve the full page(s).
When search returns multiple relevant results, use `batch-get` to fetch them all.
The API limits each call to 20 documents; `batch-get` automatically chunks larger
lists into multiple calls:

```bash
dkcli batch-get docs.cloud.google.com/storage/docs/creating-buckets docs.cloud.google.com/storage/docs/storage-classes
```

If you only need a single document, `get` works the same way:

```bash
dkcli get docs.cloud.google.com/storage/docs/creating-buckets
```

The document name is the URL path without `https://`. Full URLs also work:

```bash
dkcli get https://docs.cloud.google.com/storage/docs/creating-buckets
```

Invalid URL-like document names are rejected locally before authentication or an API request.

### When you already know the document

If you can reasonably guess the URL of the documentation page (e.g., the user gave you a link, or you know the path pattern), skip search and go straight to `dkcli get` (or `dkcli batch-get` for multiple pages). This saves a round trip.

### Getting multiple documents

When you need several pages at once, use `--outdir` to download them as files first, then check sizes before reading into context:

```bash
# Step 1: Download all documents as files
dkcli batch-get --outdir /tmp/docs docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2 docs.cloud.google.com/path/to/doc3

# Step 2: Check file sizes to plan your reading strategy
wc -c /tmp/docs/**/*.md

# Step 3: Read selectively based on size
#   - Small files (< 20KB): read the full file
#   - Large files (> 20KB): use rg or targeted reads to locate relevant sections,
#     then read each selected section with enough surrounding context
```

This workflow prevents accidentally flooding context with very large documents. Some Google docs pages can be 50KB+ of Markdown — always check sizes first.

For quick inline use without file output:

```bash
dkcli batch-get docs.cloud.google.com/path/to/doc1 docs.cloud.google.com/path/to/doc2
```

### Metadata-only retrieval

Search results now also include embedded document metadata under `results[].document` in structured output.

### Grounded answers (use with caution)

For a quick generated answer instead of raw document content, use:

```bash
dkcli answer-query "How do I create a Cloud Storage bucket?"
```

This command calls the `v1alpha:answerQuery` endpoint. It works with an API key, or with ADC if a quota project is available.

**Caveat:** The `answer-query` endpoint returns generated text with citations and references to source document chunks, but it is still a preview endpoint and is currently limited to 50 requests per day per project. For authoritative or full-context verification, **prefer the `search` + `get` workflow**. Use `answer-query` when you need a quick grounded overview, then fetch referenced documents before making or publishing a factual claim.

### Evidence-grade research

When documentation is being used to settle a compatibility question, support a
bug report, or justify a code change, treat generated answers and search chunks as
routing aids rather than final evidence:

1. Fetch the full source document with `get` or `batch-get`.
2. Read the relevant section with enough surrounding context to capture qualifiers,
   optional clauses, and exceptions.
3. Record the canonical document name or URL and the retrieval date in the working
   notes. When available, also retain document metadata such as update time.
4. Separate what the document states from conclusions inferred by the agent.
5. If multiple official pages disagree, preserve the discrepancy instead of silently
   choosing one.

For structured provenance, retrieve JSON and retain only non-sensitive metadata plus
the relevant content:

```bash
dkcli get -f json docs.cloud.google.com/path/to/doc \
  | jq '{name, uri, title, updateTime, content}'
```

Field availability can vary by document and CLI version; inspect the JSON shape
before relying on a metadata field.

## Combining with shell tools

dkcli's structured output formats make it easy to extract exactly what you need:

```bash
# Get a preview of each document's content
dkcli batch-get -f json doc1 doc2 | jq '.documents[] | {name, preview: .content[:500]}'

# Extract just document names from search results
dkcli search -f json "Cloud Storage" | jq '.results[].parent'

# Search streamed content for a relevant term
dkcli search -a -f jsonl "Pub/Sub" | jq -r '.content' | rg -n "ordering key"
```

## Output format

The default text format is best for reading into context. Use structured formats when you need to pipe or process output:

```bash
dkcli search "Spanner query syntax"                                       # text (default)
dkcli get docs.cloud.google.com/spanner/docs/query-syntax -f json         # JSON
dkcli batch-get -f txtar docs.cloud.google.com/doc1 docs.cloud.google.com/doc2  # txtar
```

## Practical tips

- **Be specific in searches.** "Cloud Storage create bucket Python" works better than "storage bucket".
- **Search results are chunks, not full pages.** Always `get` the full document when you need complete information — chunks may be missing context.
- **Document names follow URL patterns.** If you know the Google docs URL, you can construct the document name directly.
- **Rate limits are handled automatically.** dkcli has built-in rate limiting and retry logic — no need to add delays between calls.

## Error handling

Developer Knowledge API commands use Application Default Credentials (ADC) by default, except `create-api-key` which always requires ADC. If an API key environment variable is set (`DEVELOPERKNOWLEDGE_API_KEY` or `GOOGLE_API_KEY`), dkcli uses it instead of ADC.

When using local ADC, the Developer Knowledge API also requires a quota project. dkcli resolves that from `GOOGLE_CLOUD_QUOTA_PROJECT` or `quota_project_id` in the ADC file. The standard way to set that up is `gcloud auth application-default set-quota-project <project-id>`.

If dkcli fails because ADC is not available, suggest:

```bash
# Option 1: set up ADC (recommended)
gcloud auth application-default login
gcloud auth application-default set-quota-project <project-id>

# Option 2: create an API key and set it in the current shell (requires ADC once)
export DEVELOPERKNOWLEDGE_API_KEY=$(dkcli create-api-key -p <gcp-project-id> --key-only)
```

Keep generated keys out of shell profiles, command transcripts, repository files,
and agent output. Do not persist a key unless the user explicitly requests it and
chooses an appropriate secret-storage mechanism. Prefer ADC when possible.

If the user already has an API key and prefers to use it, they just need to set the environment variable:
```bash
export DEVELOPERKNOWLEDGE_API_KEY=<key>
```

Note: `get` and `batch-get` use the GA `v1` document endpoints. `answer-query` still uses `v1alpha:answerQuery`.

## Command reference

| Command | Purpose |
|---------|---------|
| `dkcli search <query>` | Search documentation chunks |
| `dkcli search -a <query>` | Search with auto-paging |
| `dkcli get <doc-name>` | Get a full document |
| `dkcli batch-get <names>...` | Get multiple documents |
| `dkcli answer-query <query>` | Generate a grounded answer |
| `dkcli create-api-key --project <id>` | Create a Developer Knowledge API key |

| Useful flags | |
|---|---|
| `-f json\|yaml\|jsonl\|txtar` | Output format (default: text) |
| `-o <file>` | Write to file |
| `--page-size N` | Results per page for search (max 20) |
| `--max-pages N` | Max pages with `-a` (default 5) |
| `--filter <expr>` | Filter search results by document metadata |
| `--outdir <dir>` | Write each doc to separate files (batch-get) |
| `--frontmatter` | Prepend YAML frontmatter to `--format=text` output (get, batch-get; `get` does not support it with `--size-only`) |
| `--key-only` | Print only the API key string (create-api-key) |
