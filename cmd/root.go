package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	dkapi "github.com/apstndb/developerknowledge-go"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

// apiLimiter enforces the 100 RPM quota with burst allowance.
var apiLimiter = rate.NewLimiter(rate.Limit(100.0/60.0), 5)

var (
	outputFormat string
	outputFile   string
	verbose      bool
)

var (
	searchBaseURL = "https://developerknowledge.googleapis.com/v1"
	// Workaround: answerQuery is still documented and served on v1alpha.
	answerQueryBaseURL = "https://developerknowledge.googleapis.com/v1alpha"
)

const defaultHTTPTimeout = dkapi.DefaultHTTPTimeout

type authMode = dkapi.AuthMode

const (
	authPreferAPIKey = dkapi.AuthPreferAPIKey
	authRequireADC   = dkapi.AuthRequireADC
)

var defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
	return dkapi.DefaultTokenSource(ctx, scopes...)
}

var adcCredentialsPath = dkapi.DefaultCredentialsPath

var rootCmd = &cobra.Command{
	Use:   "dkcli",
	Short: "CLI client for Google Developer Knowledge API",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "format", "f", "text", "output format (text, json, jsonl, yaml, txtar)")
	rootCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "write output to file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "dump response headers to stderr")
	rootCmd.SilenceUsage = true
}

// apiClient holds the configuration for making API requests.
type apiClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
	ctx     context.Context
	limiter *rate.Limiter
	verbose bool
}

func apiKeyFromEnv() string {
	return dkapi.APIKeyFromEnv()
}

func newADCHTTPClient(ctx context.Context, mode authMode, timeout time.Duration) (*http.Client, error) {
	return dkapi.NewADCHTTPClient(ctx, dkapi.AuthConfig{
		Mode:            mode,
		Timeout:         timeout,
		TokenSource:     defaultTokenSource,
		CredentialsPath: adcCredentialsPath,
	})
}

// newAPIClient creates an apiClient using the global defaults.
func newAPIClient(ctx context.Context, mode authMode) (*apiClient, error) {
	client := &apiClient{
		baseURL: searchBaseURL,
		ctx:     ctx,
		limiter: apiLimiter,
		verbose: verbose,
	}

	if mode == authPreferAPIKey {
		if apiKey := apiKeyFromEnv(); apiKey != "" {
			client.apiKey = apiKey
			client.client = &http.Client{Timeout: defaultHTTPTimeout}
			return client, nil
		}
	}

	var err error
	client.client, err = newADCHTTPClient(ctx, mode, defaultHTTPTimeout)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Document represents a Developer Knowledge API document.
type Document = dkapi.Document

func (c *apiClient) newDKClient() *dkapi.Client {
	return &dkapi.Client{
		BaseURL:       c.baseURL,
		APIKey:        c.apiKey,
		HTTPClient:    c.client,
		Context:       c.requestContext(),
		Limiter:       c.limiter,
		Verbose:       c.verbose,
		VerboseWriter: os.Stderr,
		MaxRetries:    2,
	}
}

func (c *apiClient) doAPIRequest(method, url string, body []byte, contentType string) ([]byte, error) {
	return c.newDKClient().DoAPIRequest(method, url, body, contentType)
}

func (c *apiClient) requestContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *apiClient) doGet(url string) ([]byte, error) {
	return c.doAPIRequest(http.MethodGet, url, nil, "")
}

func (c *apiClient) doJSONPost(url string, body []byte) ([]byte, error) {
	return c.doAPIRequest(http.MethodPost, url, body, "application/json")
}

func normalizeDocName(name string) string {
	return dkapi.NormalizeDocName(name)
}

// outWriter returns the writer for command output.
// If file is non-empty, it opens the file and returns it with a closer.
// Otherwise it returns stdout.
func outWriter(file string) (io.Writer, func(), error) {
	if file == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(file)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}

// writeFormatted encodes v in the given format to w.
func writeFormatted(w io.Writer, format string, v any) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case "jsonl":
		return json.NewEncoder(w).Encode(v)
	case "yaml":
		return yaml.NewEncoder(w).Encode(v)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

func docContentLines(content string) int {
	lines := strings.Count(content, "\n")
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
}

// printDocSummary prints a one-line summary of a document to w.
func printDocSummary(w io.Writer, doc *Document) {
	fmt.Fprintf(w, "%s (%d bytes, %d lines)\n",
		doc.Name, len(doc.Content), docContentLines(doc.Content))
}

// printDocsSummary prints a per-document summary plus a total line to w.
func printDocsSummary(w io.Writer, docs []Document) {
	totalBytes := 0
	totalLines := 0
	for i := range docs {
		printDocSummary(w, &docs[i])
		totalBytes += len(docs[i].Content)
		totalLines += docContentLines(docs[i].Content)
	}
	if len(docs) > 1 {
		fmt.Fprintf(w, "total: %d documents, %d bytes, %d lines\n", len(docs), totalBytes, totalLines)
	}
}

// txtarEntry formats a single txtar file entry.
func txtarEntry(name, content string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "-- %s --\n", name)
	sb.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		sb.WriteByte('\n')
	}
	return sb.String()
}
