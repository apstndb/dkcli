package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
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

func runBatchGet(cmd *cobra.Command, args []string) error {
	apiKey, err := getAPIKey()
	if err != nil {
		return err
	}

	params := url.Values{}
	for _, arg := range args {
		params.Add("names", normalizeDocName(arg))
	}

	reqURL := baseURL + "/documents:batchGet?" + params.Encode()

	body, err := doGet(reqURL, apiKey)
	if err != nil {
		return err
	}

	var resp batchGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}

	switch outputFormat {
	case "text":
		var sb strings.Builder
		for i, doc := range resp.Documents {
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
		for _, doc := range resp.Documents {
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
		for _, doc := range resp.Documents {
			if err := enc.Encode(doc); err != nil {
				return err
			}
		}
		return nil
	default:
		return printFormatted(resp)
	}
}
