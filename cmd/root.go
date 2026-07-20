package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	dkapi "github.com/apstndb/developerknowledge-go"
	"github.com/spf13/cobra"
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

var version = "dev"

var (
	searchBaseURL      = dkapi.DefaultV1BaseURL
	answerQueryBaseURL = dkapi.DefaultV1BaseURL
)

const defaultHTTPTimeout = dkapi.DefaultHTTPTimeout

type authMode = dkapi.AuthMode

const (
	authPreferAPIKey = dkapi.AuthPreferAPIKey
	authRequireADC   = dkapi.AuthRequireADC
)

// Nil defaults preserve dkapi's ADC discovery, including credentials-file
// metadata such as quota_project_id and metadata-server fallback. Tests may
// override either seam for deterministic authentication behavior.
var defaultTokenSource dkapi.TokenSourceFunc

var adcCredentialsPath func() string

var rootCmd = &cobra.Command{
	Use:     "dkcli",
	Short:   "CLI client for Google Developer Knowledge API",
	Version: commandVersion(),
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return validateOutputFormat(outputFormat)
	},
}

func Execute() {
	ExecuteContext(context.Background())
}

func ExecuteContext(ctx context.Context) {
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "format", "f", "text", "output format (text, json, jsonl, yaml, txtar)")
	rootCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "write output to file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "dump response headers to stderr")
	rootCmd.SilenceUsage = true
}

func commandVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func validateOutputFormat(format string) error {
	switch format {
	case "text", "json", "jsonl", "yaml", "txtar":
		return nil
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
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

func newADCHTTPClient(ctx context.Context, mode authMode, timeout time.Duration, baseURL string) (*http.Client, error) {
	return dkapi.NewADCHTTPClient(ctx, authConfig(mode, timeout, baseURL))
}

func authConfig(mode authMode, timeout time.Duration, baseURL string) dkapi.AuthConfig {
	cfg := dkapi.AuthConfig{
		Mode:          mode,
		Timeout:       timeout,
		AllowedOrigin: baseURL,
	}
	if defaultTokenSource != nil {
		cfg.TokenSource = defaultTokenSource
	}
	if adcCredentialsPath != nil {
		cfg.CredentialsPath = adcCredentialsPath
	}
	return cfg
}

// newAPIClient creates an apiClient using the global defaults.
func newAPIClient(ctx context.Context, mode authMode) (*apiClient, error) {
	return newAPIClientForBaseURL(ctx, mode, searchBaseURL)
}

// newAPIClientForBaseURL creates an apiClient whose requests and authentication
// origin guard use the same API base URL.
func newAPIClientForBaseURL(ctx context.Context, mode authMode, baseURL string) (*apiClient, error) {
	client := &apiClient{
		baseURL: baseURL,
		ctx:     ctx,
		limiter: apiLimiter,
		verbose: verbose,
	}

	httpClient, apiKey, err := dkapi.NewAuthenticatedHTTPClient(ctx, authConfig(mode, defaultHTTPTimeout, baseURL))
	if err != nil {
		return nil, err
	}
	client.apiKey = apiKey
	client.client = httpClient
	return client, nil
}

// Document represents a Developer Knowledge API document.
type Document = dkapi.Document

func (c *apiClient) newDKClient() *dkapi.Client {
	return &dkapi.Client{
		BaseURL:       c.baseURL,
		APIKey:        c.apiKey,
		HTTPClient:    c.client,
		Limiter:       c.limiter,
		Verbose:       c.verbose,
		VerboseWriter: os.Stderr,
		MaxRetries:    2,
	}
}

func (c *apiClient) doAPIRequest(method, url string, body []byte, contentType string) ([]byte, error) {
	return c.newDKClient().DoAPIRequest(c.requestContext(), method, url, body, contentType)
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
func outWriter(file string) (io.Writer, func() error, error) {
	if file == "" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.Create(file)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

func finishOutput(writeErr error, close func() error) error {
	closeErr := close()
	if writeErr != nil {
		if closeErr != nil {
			return errors.Join(writeErr, closeErr)
		}
		return writeErr
	}
	return closeErr
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
