package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const batchGetMaxDocs = 20

var (
	outDir           string
	batchFrontmatter bool
)

var batchGetCmd = &cobra.Command{
	Use:   "batch-get <document-name>...",
	Short: "Retrieve multiple documents",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runBatchGet,
}

func init() {
	batchGetCmd.Flags().StringVar(&outDir, "outdir", "", "write each document to a separate file under this directory")
	batchGetCmd.Flags().BoolVar(&batchFrontmatter, "frontmatter", false, "prepend YAML frontmatter to content (text format only)")
	rootCmd.AddCommand(batchGetCmd)
}

type batchGetResponse struct {
	Documents []Document `json:"documents" yaml:"documents"`
}

// fetchBatchGet makes a single batchGet API call for the given document names.
func (c *apiClient) fetchBatchGet(names []string) ([]Document, error) {
	params := url.Values{}
	for _, name := range names {
		params.Add("names", name)
	}

	reqURL := c.baseURL + "/documents:batchGet?" + params.Encode()

	body, err := c.doGet(reqURL)
	if err != nil {
		return nil, err
	}

	var resp batchGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Documents, nil
}

// isBisectable reports whether the error is a client error (4xx) that
// can be narrowed down by bisecting the request into smaller batches.
// Network errors, 5xx, and rate-limit exhaustion are not bisectable.
func isBisectable(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code >= 400 && ae.Code < 500
}

// fetchBatchBisect fetches documents, bisecting on bisectable errors to
// identify individual failures.
// Returns:
//   - docs: successfully fetched documents
//   - docErrs: per-document errors identified by bisection
//   - fatal: non-bisectable error (network failure, 5xx, etc.)
func (c *apiClient) fetchBatchBisect(names []string) ([]Document, []error, error) {
	docs, err := c.fetchBatchGet(names)
	if err == nil {
		return docs, nil, nil
	}

	if !isBisectable(err) {
		return nil, nil, err
	}

	// Single document: report the error with the document name.
	if len(names) == 1 {
		return nil, []error{fmt.Errorf("%s: %w", names[0], err)}, nil
	}

	if c.verbose {
		fmt.Fprintf(os.Stderr, "Batch failed (%d documents), bisecting to identify failures...\n", len(names))
	}

	// Split and retry each half.
	mid := len(names) / 2
	leftDocs, leftErrs, leftFatal := c.fetchBatchBisect(names[:mid])
	if leftFatal != nil {
		return nil, nil, leftFatal
	}
	rightDocs, rightErrs, rightFatal := c.fetchBatchBisect(names[mid:])
	if rightFatal != nil {
		return leftDocs, nil, rightFatal
	}

	return append(leftDocs, rightDocs...), append(leftErrs, rightErrs...), nil
}

// formatExtension returns the file extension for the given output format.
func formatExtension(format string) string {
	switch format {
	case "json":
		return ".json"
	case "jsonl":
		return ".jsonl"
	case "yaml":
		return ".yaml"
	case "txtar":
		return ".txtar"
	default:
		return ".md"
	}
}

// docFilePath builds the output file path by stripping the "documents/" prefix
// from the document name and appending the format extension under outDir.
func docFilePath(outDir, docName, format string) string {
	name := strings.TrimPrefix(docName, "documents/")
	return filepath.Join(outDir, name+formatExtension(format))
}

// formatDocForFile formats a single document for file output.
func formatDocForFile(doc *Document, format string, frontmatter bool) ([]byte, error) {
	switch format {
	case "text":
		if frontmatter {
			s, err := formatDocWithFrontmatter(doc)
			if err != nil {
				return nil, err
			}
			return []byte(s), nil
		}
		return []byte(formatDocText(doc)), nil
	case "json":
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	case "jsonl":
		b, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	case "yaml":
		return yaml.Marshal(doc)
	case "txtar":
		return []byte(txtarEntry(doc.Name, doc.Content)), nil
	default:
		return nil, fmt.Errorf("unknown format: %s", format)
	}
}

// writeBatchOutdir writes each document as an individual file under dir.
// Subdirectories are created as needed. Individual write failures are collected
// and reported as a summary; processing continues on error.
// After writing, a summary table of document name, path, bytes, and lines is
// printed to stderr.
func writeBatchOutdir(docs []Document, dir, format string, frontmatter bool) error {
	type fileStat struct {
		name  string
		path  string
		bytes int
		lines int
	}
	var stats []fileStat
	var writeErrs []error
	for i := range docs {
		doc := &docs[i]
		path := docFilePath(dir, doc.Name, format)

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			writeErrs = append(writeErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}

		data, err := formatDocForFile(doc, format, frontmatter)
		if err != nil {
			writeErrs = append(writeErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}

		if err := os.WriteFile(path, data, 0o644); err != nil {
			writeErrs = append(writeErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}

		lines := strings.Count(string(data), "\n")
		stats = append(stats, fileStat{name: doc.Name, path: path, bytes: len(data), lines: lines})
	}

	// Print summary.
	if len(stats) > 0 {
		totalBytes := 0
		totalLines := 0
		for _, s := range stats {
			fmt.Fprintf(os.Stderr, "%s → %s (%d bytes, %d lines)\n", s.name, s.path, s.bytes, s.lines)
			totalBytes += s.bytes
			totalLines += s.lines
		}
		fmt.Fprintf(os.Stderr, "total: %d files, %d bytes, %d lines\n", len(stats), totalBytes, totalLines)
	}

	if len(writeErrs) > 0 {
		fmt.Fprintf(os.Stderr, "ERROR: %d/%d files failed to write:\n", len(writeErrs), len(docs))
		for _, e := range writeErrs {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
		return fmt.Errorf("%d/%d files failed to write", len(writeErrs), len(docs))
	}
	return nil
}

// printBatchOutput writes the documents in the requested output format.
func printBatchOutput(docs []Document, format string, frontmatter bool) error {
	resp := batchGetResponse{Documents: docs}

	switch format {
	case "text":
		var sb strings.Builder
		for i := range docs {
			doc := &docs[i]
			if frontmatter {
				// Frontmatter blocks are self-delimiting; no separator needed.
				s, err := formatDocWithFrontmatter(doc)
				if err != nil {
					return err
				}
				sb.WriteString(s)
			} else {
				if i > 0 {
					sb.WriteString("\n---\n")
				}
				fmt.Fprintf(&sb, "# %s\n\n", doc.Name)
				sb.WriteString(doc.Content)
				if doc.Content != "" && doc.Content[len(doc.Content)-1] != '\n' {
					sb.WriteByte('\n')
				}
			}
		}
		return printText(sb.String())
	case "txtar":
		var sb strings.Builder
		for _, doc := range docs {
			sb.WriteString(txtarEntry(doc.Name, doc.Content))
		}
		return printText(sb.String())
	case "jsonl":
		w, closer, err := outWriter()
		if err != nil {
			return err
		}
		defer closer()
		enc := json.NewEncoder(w)
		for _, doc := range docs {
			if err := enc.Encode(doc); err != nil {
				return err
			}
		}
		return nil
	default:
		return printFormatted(resp)
	}
}

func runBatchGet(cmd *cobra.Command, args []string) error {
	if outDir != "" && outputFile != "" {
		return fmt.Errorf("--outdir and --output are mutually exclusive")
	}
	if batchFrontmatter && outputFormat != "text" {
		return fmt.Errorf("--frontmatter can only be used with text format")
	}

	apiKey, err := getAPIKey()
	if err != nil {
		return err
	}

	names := make([]string, len(args))
	for i, arg := range args {
		names[i] = normalizeDocName(arg)
	}

	client := newAPIClient(apiKey)

	var docs []Document
	var docErrs []error
	for i := 0; i < len(names); i += batchGetMaxDocs {
		end := min(i+batchGetMaxDocs, len(names))
		chunk := names[i:end]

		if verbose && len(names) > batchGetMaxDocs {
			fmt.Fprintf(os.Stderr, "Fetching chunk %d-%d of %d documents...\n", i+1, end, len(names))
		}

		d, e, fatal := client.fetchBatchBisect(chunk)
		if fatal != nil {
			return fatal
		}
		docs = append(docs, d...)
		docErrs = append(docErrs, e...)
	}

	// All documents failed.
	if len(docs) == 0 && len(docErrs) > 0 {
		fmt.Fprintf(os.Stderr, "ERROR: %d/%d documents failed:\n", len(docErrs), len(docErrs))
		for _, e := range docErrs {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
		return fmt.Errorf("all %d documents failed", len(docErrs))
	}

	var outputErr error
	if outDir != "" {
		outputErr = writeBatchOutdir(docs, outDir, outputFormat, batchFrontmatter)
	} else {
		outputErr = printBatchOutput(docs, outputFormat, batchFrontmatter)
	}
	if outputErr != nil {
		return outputErr
	}

	// Partial failure: print summary after output so it's visible at the end.
	if len(docErrs) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: %d/%d documents failed:\n", len(docErrs), len(names))
		for _, e := range docErrs {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
		return fmt.Errorf("%d/%d documents failed", len(docErrs), len(names))
	}

	return nil
}
