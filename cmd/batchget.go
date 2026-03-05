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

var (
	outDir           string
	batchFrontmatter bool
)

var batchGetCmd = &cobra.Command{
	Use:   "batch-get <document-name>...",
	Short: "Retrieve multiple documents (max 20)",
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
func fetchBatchGet(apiKey string, names []string) ([]Document, error) {
	params := url.Values{}
	for _, name := range names {
		params.Add("names", name)
	}

	reqURL := baseURL + "/documents:batchGet?" + params.Encode()

	body, err := doGet(reqURL, apiKey)
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
func fetchBatchBisect(apiKey string, names []string) ([]Document, []error, error) {
	docs, err := fetchBatchGet(apiKey, names)
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

	if verbose {
		fmt.Fprintf(os.Stderr, "Batch failed (%d documents), bisecting to identify failures...\n", len(names))
	}

	// Split and retry each half.
	mid := len(names) / 2
	leftDocs, leftErrs, leftFatal := fetchBatchBisect(apiKey, names[:mid])
	if leftFatal != nil {
		return nil, nil, leftFatal
	}
	rightDocs, rightErrs, rightFatal := fetchBatchBisect(apiKey, names[mid:])
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

// writeBatchOutdir writes each document as an individual file under outDir.
// Subdirectories are created as needed. Individual write failures are collected
// and reported as a summary; processing continues on error.
func writeBatchOutdir(docs []Document) error {
	var writeErrs []error
	for i := range docs {
		doc := &docs[i]
		path := docFilePath(outDir, doc.Name, outputFormat)

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			writeErrs = append(writeErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}

		data, err := formatDocForFile(doc, outputFormat, batchFrontmatter)
		if err != nil {
			writeErrs = append(writeErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}

		if err := os.WriteFile(path, data, 0o644); err != nil {
			writeErrs = append(writeErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}

		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
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
func printBatchOutput(docs []Document) error {
	resp := batchGetResponse{Documents: docs}

	switch outputFormat {
	case "text":
		var sb strings.Builder
		for i := range docs {
			doc := &docs[i]
			if batchFrontmatter {
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

	docs, docErrs, fatal := fetchBatchBisect(apiKey, names)
	if fatal != nil {
		return fatal
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
		outputErr = writeBatchOutdir(docs)
	} else {
		outputErr = printBatchOutput(docs)
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
