package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
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

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
const defaultHTTPTimeout = time.Minute

type authMode int

const (
	authPreferAPIKey authMode = iota
	authRequireADC
)

var defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
	return google.DefaultTokenSource(ctx, scopes...)
}

type adcCredentialsMetadata struct {
	Type           string `json:"type"`
	QuotaProjectID string `json:"quota_project_id"`
}

var adcCredentialsPath = func() string {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return path
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}
	return defaultADCCredentialsPath(runtime.GOOS, homeDir, os.Getenv("APPDATA"))
}

func defaultADCCredentialsPath(goos, homeDir, appData string) string {
	if goos == "windows" {
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "gcloud", "application_default_credentials.json")
	}
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, ".config", "gcloud", "application_default_credentials.json")
}

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
	if key := os.Getenv("DEVELOPERKNOWLEDGE_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		return key
	}
	return ""
}

func newADCHTTPClient(ctx context.Context, mode authMode, timeout time.Duration) (*http.Client, error) {
	tokenSource, err := defaultTokenSource(ctx, cloudPlatformScope)
	if err != nil {
		if mode == authPreferAPIKey {
			return nil, fmt.Errorf("set DEVELOPERKNOWLEDGE_API_KEY or GOOGLE_API_KEY, or configure ADC with 'gcloud auth application-default login': %w", err)
		}
		return nil, fmt.Errorf("get credentials: %w (run 'gcloud auth application-default login')", err)
	}

	client := oauth2.NewClient(ctx, tokenSource)
	if timeout > 0 {
		client.Timeout = timeout
	}

	quotaProject, metadata := resolveQuotaProjectID()
	if quotaProject == "" && metadata.Type == "authorized_user" {
		return nil, fmt.Errorf("ADC requires a quota project; run 'gcloud auth application-default set-quota-project <project-id>' or set GOOGLE_CLOUD_QUOTA_PROJECT")
	}
	if quotaProject != "" {
		baseTransport := client.Transport
		if baseTransport == nil {
			baseTransport = http.DefaultTransport
		}
		client.Transport = &quotaProjectTransport{
			Base:    baseTransport,
			Project: quotaProject,
		}
	}
	return client, nil
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
type Document struct {
	Name        string `json:"name" yaml:"name"`
	URI         string `json:"uri" yaml:"uri"`
	Content     string `json:"content,omitempty" yaml:"content,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	DataSource  string `json:"dataSource,omitempty" yaml:"data_source,omitempty"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty" yaml:"update_time,omitempty"`
	View        string `json:"view,omitempty" yaml:"view,omitempty"`
}

// apiError represents a non-OK HTTP response from the API.
type apiError struct {
	Code    int
	Status  string
	Message string
}

func (e *apiError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("API error %d (%s): %s", e.Code, e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Message)
}

// rateLimitError is returned when the API responds with HTTP 429.
type rateLimitError struct {
	RetryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited (retry after %v)", e.RetryAfter)
	}
	return "rate limited"
}

// parseRetryAfter extracts the Retry-After header value as a duration.
// Returns 0 if the header is absent or unparseable.
func parseRetryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// checkResponse reads the response body, closes it, and returns the body bytes.
// If the status code is not 200, it attempts to parse a Google API error JSON
// and returns a descriptive error. HTTP 429 returns a *rateLimitError.
func checkResponse(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &rateLimitError{RetryAfter: parseRetryAfter(resp)}
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, &apiError{Code: apiErr.Error.Code, Status: apiErr.Error.Status, Message: apiErr.Error.Message}
		}
		return nil, &apiError{Code: resp.StatusCode, Message: string(body)}
	}

	return body, nil
}

func (c *apiClient) doAPIRequest(method, url string, body []byte, contentType string) ([]byte, error) {
	const maxRetries = 3
	backoff := 1 * time.Second
	ctx := c.requestContext()
	var retryErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, requestBody)
		if err != nil {
			return nil, err
		}
		if c.apiKey != "" {
			req.Header.Set("x-goog-api-key", c.apiKey)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			if resp != nil && resp.Body != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			return nil, err
		}

		if c.verbose {
			dump, _ := httputil.DumpResponse(resp, false)
			fmt.Fprintf(os.Stderr, "%s", dump)
		}

		respBody, err := checkResponse(resp)
		var rlErr *rateLimitError
		if errors.As(err, &rlErr) {
			retryErr = err
			if attempt == maxRetries-1 {
				break
			}
			wait := backoff
			if rlErr.RetryAfter > 0 {
				wait = rlErr.RetryAfter
			}
			if c.verbose {
				fmt.Fprintf(os.Stderr, "Rate limited, retrying after %v...\n", wait)
			}
			if err := sleepContext(ctx, wait); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		return respBody, err
	}
	return nil, retryErr
}

func (c *apiClient) requestContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func sleepContext(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *apiClient) doGet(url string) ([]byte, error) {
	return c.doAPIRequest(http.MethodGet, url, nil, "")
}

func (c *apiClient) doJSONPost(url string, body []byte) ([]byte, error) {
	return c.doAPIRequest(http.MethodPost, url, body, "application/json")
}

func normalizeDocName(name string) string {
	name = strings.TrimPrefix(name, "https://")
	name = strings.TrimPrefix(name, "http://")
	if !strings.HasPrefix(name, "documents/") {
		name = "documents/" + name
	}
	return name
}

func loadADCCredentialsMetadata() adcCredentialsMetadata {
	path := adcCredentialsPath()
	if path == "" {
		return adcCredentialsMetadata{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return adcCredentialsMetadata{}
	}
	var cfg adcCredentialsMetadata
	if err := json.Unmarshal(data, &cfg); err != nil {
		return adcCredentialsMetadata{}
	}
	return cfg
}

func resolveQuotaProjectID() (string, adcCredentialsMetadata) {
	if p := os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT"); p != "" {
		return p, adcCredentialsMetadata{}
	}

	cfg := loadADCCredentialsMetadata()
	return cfg.QuotaProjectID, cfg
}

type quotaProjectTransport struct {
	Base    http.RoundTripper
	Project string
}

func (t *quotaProjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("x-goog-user-project", t.Project)
	return t.Base.RoundTrip(req)
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
