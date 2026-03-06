package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"
	"time"

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

var baseURL = "https://developerknowledge.googleapis.com/v1alpha"

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
	limiter *rate.Limiter
	verbose bool
}

// newAPIClient creates an apiClient using the global defaults.
func newAPIClient(apiKey string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  http.DefaultClient,
		limiter: apiLimiter,
		verbose: verbose,
	}
}

// Document represents a Developer Knowledge API document.
type Document struct {
	Name        string `json:"name" yaml:"name"`
	URI         string `json:"uri" yaml:"uri"`
	Content     string `json:"content" yaml:"content"`
	Description string `json:"description" yaml:"description"`
}

func getAPIKey() (string, error) {
	if key := os.Getenv("DEVELOPERKNOWLEDGE_API_KEY"); key != "" {
		return key, nil
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("set DEVELOPERKNOWLEDGE_API_KEY or GOOGLE_API_KEY")
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

func (c *apiClient) doGet(url string) ([]byte, error) {
	const maxRetries = 3
	backoff := 1 * time.Second

	for attempt := range maxRetries {
		if err := c.limiter.Wait(context.Background()); err != nil {
			return nil, err
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", c.apiKey)

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}

		if c.verbose {
			dump, _ := httputil.DumpResponse(resp, false)
			fmt.Fprintf(os.Stderr, "%s", dump)
		}

		body, err := checkResponse(resp)
		var rlErr *rateLimitError
		if errors.As(err, &rlErr) && attempt < maxRetries-1 {
			wait := backoff
			if rlErr.RetryAfter > 0 {
				wait = rlErr.RetryAfter
			}
			if c.verbose {
				fmt.Fprintf(os.Stderr, "Rate limited, retrying after %v...\n", wait)
			}
			time.Sleep(wait)
			backoff *= 2
			continue
		}
		return body, err
	}
	// unreachable
	return nil, fmt.Errorf("exceeded max retries")
}

func normalizeDocName(name string) string {
	name = strings.TrimPrefix(name, "https://")
	name = strings.TrimPrefix(name, "http://")
	if !strings.HasPrefix(name, "documents/") {
		name = "documents/" + name
	}
	return name
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
