package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/apstndb/developerknowledge-go"
	"github.com/spf13/cobra"
)

var (
	pageSize     int
	pageToken    string
	autoPaging   bool
	maxPages     int
	searchFilter string
)

var searchCmd = &cobra.Command{
	Use:   "search <query>...",
	Short: "Search developer documentation chunks",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().IntVar(&pageSize, "page-size", 0, "max results to return (max: 20, default is set by the API)")
	searchCmd.Flags().StringVar(&pageToken, "page-token", "", "pagination token from previous response")
	searchCmd.Flags().BoolVarP(&autoPaging, "auto-paging", "a", false, "automatically fetch subsequent pages")
	searchCmd.Flags().IntVar(&maxPages, "max-pages", 5, "max pages to fetch with --auto-paging (0 for unlimited)")
	searchCmd.Flags().StringVar(&searchFilter, "filter", "", "AIP-160 filter on document metadata")
	rootCmd.AddCommand(searchCmd)
}

// DocumentChunk represents a search result chunk.
type DocumentChunk = dkapi.DocumentChunk

type searchResponse struct {
	Results       []DocumentChunk `json:"results" yaml:"results"`
	NextPageToken string          `json:"nextPageToken,omitempty" yaml:"next_page_token,omitempty"`
}

// runSearchJSONL streams search results as JSONL, writing each chunk
// immediately as pages are fetched rather than buffering all results.
func runSearchJSONL(w io.Writer, client *apiClient, query string) error {
	enc := json.NewEncoder(w)
	token := pageToken
	pages := 0
	total := 0
	start := time.Now()
	var lastNextPageToken string

	for {
		resp, err := client.fetchSearchPage(query, pageSize, token, searchFilter)
		if err != nil {
			return err
		}
		pages++

		for _, chunk := range resp.Results {
			if err := enc.Encode(chunk); err != nil {
				return err
			}
		}
		total += len(resp.Results)
		lastNextPageToken = resp.NextPageToken

		if !autoPaging || resp.NextPageToken == "" {
			break
		}
		if maxPages > 0 && pages >= maxPages {
			fmt.Fprintf(os.Stderr, "WARNING: reached max pages (%d), use --max-pages to increase\n", maxPages)
			break
		}
		token = resp.NextPageToken
	}

	if autoPaging {
		fmt.Fprintf(os.Stderr, "Fetched %d pages (%d results) in %v\n", pages, total, time.Since(start).Truncate(time.Millisecond))
	}

	if lastNextPageToken != "" {
		fmt.Fprintf(os.Stderr, "Next page token: %s\n", lastNextPageToken)
	}

	return nil
}

func (c *apiClient) fetchSearchPage(query string, size int, token, filter string) (*searchResponse, error) {
	params := url.Values{}
	params.Set("query", query)
	if size > 0 {
		params.Set("pageSize", strconv.Itoa(size))
	}
	if token != "" {
		params.Set("pageToken", token)
	}
	if filter != "" {
		params.Set("filter", filter)
	}

	body, err := c.doGet(c.baseURL + "/documents:searchDocumentChunks?" + params.Encode())
	if err != nil {
		return nil, err
	}

	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func runSearch(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd.Context(), authPreferAPIKey)
	if err != nil {
		return err
	}

	// When auto-paging, default to max page size to minimize requests.
	if autoPaging && !cmd.Flags().Changed("page-size") {
		pageSize = 20
	}

	query := strings.Join(args, " ")

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}
	defer closer()

	if outputFormat == "jsonl" {
		return runSearchJSONL(w, client, query)
	}

	var allResults searchResponse
	token := pageToken
	pages := 0
	start := time.Now()

	for {
		resp, err := client.fetchSearchPage(query, pageSize, token, searchFilter)
		if err != nil {
			return err
		}
		pages++

		allResults.Results = append(allResults.Results, resp.Results...)
		allResults.NextPageToken = resp.NextPageToken

		if !autoPaging || resp.NextPageToken == "" {
			break
		}
		if maxPages > 0 && pages >= maxPages {
			fmt.Fprintf(os.Stderr, "WARNING: reached max pages (%d), use --max-pages to increase\n", maxPages)
			break
		}
		token = resp.NextPageToken
	}

	if autoPaging {
		fmt.Fprintf(os.Stderr, "Fetched %d pages (%d results) in %v\n", pages, len(allResults.Results), time.Since(start).Truncate(time.Millisecond))
	}

	if allResults.NextPageToken != "" && (outputFormat == "text" || outputFormat == "txtar") {
		fmt.Fprintf(os.Stderr, "Next page token: %s\n", allResults.NextPageToken)
	}

	switch outputFormat {
	case "text":
		var sb strings.Builder
		for i, chunk := range allResults.Results {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			fmt.Fprintf(&sb, "## %s [%s]\n\n", chunk.Parent, chunk.ID)
			if chunk.Document != nil {
				if chunk.Document.Title != "" {
					fmt.Fprintf(&sb, "Title: %s\n", chunk.Document.Title)
				}
				if chunk.Document.DataSource != "" {
					fmt.Fprintf(&sb, "Data source: %s\n", chunk.Document.DataSource)
				}
				if chunk.Document.UpdateTime != "" {
					fmt.Fprintf(&sb, "Updated: %s\n", chunk.Document.UpdateTime)
				}
				if chunk.Document.URI != "" {
					fmt.Fprintf(&sb, "URI: %s\n", chunk.Document.URI)
				}
				if chunk.Document.Title != "" || chunk.Document.DataSource != "" || chunk.Document.UpdateTime != "" || chunk.Document.URI != "" {
					sb.WriteByte('\n')
				}
			}
			sb.WriteString(chunk.Content)
			sb.WriteByte('\n')
		}
		_, err := fmt.Fprint(w, sb.String())
		return err
	case "txtar":
		var sb strings.Builder
		for _, chunk := range allResults.Results {
			name := fmt.Sprintf("%s#%s", chunk.Parent, chunk.ID)
			sb.WriteString(txtarEntry(name, chunk.Content))
		}
		_, err := fmt.Fprint(w, sb.String())
		return err
	default:
		return writeFormatted(w, outputFormat, allResults)
	}
}
