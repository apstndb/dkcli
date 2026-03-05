package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var batchGetCmd = &cobra.Command{
	Use:   "batch-get <document-name>...",
	Short: "Retrieve multiple documents (max 20)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runBatchGet,
}

func init() {
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

// printBatchOutput writes the documents in the requested output format.
func printBatchOutput(docs []Document) error {
	resp := batchGetResponse{Documents: docs}

	switch outputFormat {
	case "text":
		var sb strings.Builder
		for i, doc := range docs {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			fmt.Fprintf(&sb, "# %s\n\n", doc.Name)
			sb.WriteString(doc.Content)
			if doc.Content != "" && doc.Content[len(doc.Content)-1] != '\n' {
				sb.WriteByte('\n')
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

	if err := printBatchOutput(docs); err != nil {
		return err
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
