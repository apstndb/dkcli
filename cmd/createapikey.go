package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var (
	projectID   string
	displayName string
	keyOnly     bool
)

var (
	apiKeysBaseURL               = "https://apikeys.googleapis.com/v2"
	createAPIKeyPollInterval     = 2 * time.Second
	createAPIKeyOperationTimeout = defaultHTTPTimeout
)

var createAPIKeyCmd = &cobra.Command{
	Use:   "create-api-key",
	Short: "Create an API key restricted to Developer Knowledge API",
	Long: `Create a Google API key restricted to the Developer Knowledge API.

The key is created under the specified Google Cloud project and scoped
to developerknowledge.googleapis.com only. You need application default
credentials (run 'gcloud auth application-default login' first).

For advanced restrictions (IP allowlists, HTTP referrers, etc.), use
'gcloud services api-keys create' instead.`,
	Example: `  # Create an API key
  dkcli create-api-key --project my-project

  # Create and set in current shell
  export DEVELOPERKNOWLEDGE_API_KEY=$(dkcli create-api-key -p my-project --key-only)

  # Persist in shell profile (review before sourcing)
  echo "export DEVELOPERKNOWLEDGE_API_KEY=$(dkcli create-api-key -p my-project --key-only)" >> ~/.zshrc

  # View full key details as JSON
  dkcli create-api-key -p my-project -f json`,
	RunE: runCreateAPIKey,
}

func init() {
	createAPIKeyCmd.Flags().StringVarP(&projectID, "project", "p", "", "Google Cloud project ID (env: GOOGLE_CLOUD_PROJECT)")
	createAPIKeyCmd.Flags().StringVarP(&displayName, "display-name", "d", "dkcli", "display name for the API key")
	createAPIKeyCmd.Flags().BoolVar(&keyOnly, "key-only", false, "print only the API key string (for use in scripts)")
	rootCmd.AddCommand(createAPIKeyCmd)
}

type createKeyRequest struct {
	DisplayName  string          `json:"displayName"`
	Restrictions keyRestrictions `json:"restrictions"`
}

type keyRestrictions struct {
	APITargets []keyAPITarget `json:"apiTargets"`
}

type keyAPITarget struct {
	Service string `json:"service"`
}

type lroOperation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Error    *lroError       `json:"error,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type lroError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type keyStringResult struct {
	KeyString string `json:"keyString"`
}

func apiKeyOperationTimeoutError(opName string) error {
	if opName == "" {
		return fmt.Errorf("operation timed out")
	}
	return fmt.Errorf("operation timed out: %s", opName)
}

func resolveProjectID() string {
	if projectID != "" {
		return projectID
	}
	if p := os.Getenv("GOOGLE_CLOUD_PROJECT"); p != "" {
		return p
	}
	if p := os.Getenv("CLOUDSDK_CORE_PROJECT"); p != "" {
		return p
	}
	return ""
}

func newAPIKeysClient(ctx context.Context) (*http.Client, error) {
	ts, err := defaultTokenSource(ctx, cloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("get credentials: %w (run 'gcloud auth application-default login')", err)
	}

	client := oauth2.NewClient(ctx, ts)

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

func doAPIKeysRequest(ctx context.Context, client *http.Client, method, url string, body []byte, contentType string) ([]byte, error) {
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return checkResponse(resp)
}

func runCreateAPIKey(cmd *cobra.Command, args []string) error {
	project := resolveProjectID()
	if project == "" {
		return fmt.Errorf("--project is required (or set GOOGLE_CLOUD_PROJECT)")
	}

	ctx := cmd.Context()
	opCtx, cancel := context.WithTimeout(ctx, createAPIKeyOperationTimeout)
	defer cancel()

	client, err := newAPIKeysClient(opCtx)
	if err != nil {
		return err
	}

	// Create the API key
	reqBody, err := json.Marshal(createKeyRequest{
		DisplayName: displayName,
		Restrictions: keyRestrictions{
			APITargets: []keyAPITarget{
				{Service: "developerknowledge.googleapis.com"},
			},
		},
	})
	if err != nil {
		return err
	}

	createURL := fmt.Sprintf("%s/projects/%s/locations/global/keys", apiKeysBaseURL, project)
	body, err := doAPIKeysRequest(opCtx, client, http.MethodPost, createURL, reqBody, "application/json")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return apiKeyOperationTimeoutError("")
		}
		return err
	}

	var op lroOperation
	if err := json.Unmarshal(body, &op); err != nil {
		return err
	}

	for !op.Done {
		if err := sleepContext(opCtx, createAPIKeyPollInterval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return apiKeyOperationTimeoutError(op.Name)
			}
			return err
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "Polling operation %s...\n", op.Name)
		}
		pollURL := fmt.Sprintf("%s/%s", apiKeysBaseURL, op.Name)
		pollBody, err := doAPIKeysRequest(opCtx, client, http.MethodGet, pollURL, nil, "")
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return apiKeyOperationTimeoutError(op.Name)
			}
			return err
		}
		if err := json.Unmarshal(pollBody, &op); err != nil {
			return err
		}
	}

	if op.Error != nil {
		return fmt.Errorf("operation failed: %s", op.Error.Message)
	}

	// Parse full key resource from LRO response.
	var keyResp map[string]any
	if err := json.Unmarshal(op.Response, &keyResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	keyName, ok := keyResp["name"].(string)
	if !ok {
		return fmt.Errorf("unexpected response: missing key name")
	}

	// Log restrictions so the user can verify the key is properly scoped.
	if restrictions, ok := keyResp["restrictions"]; ok {
		b, _ := json.MarshalIndent(restrictions, "", "  ")
		fmt.Fprintf(os.Stderr, "Restrictions: %s\n", b)
	} else {
		fmt.Fprintln(os.Stderr, "WARNING: key has no restrictions")
	}

	// Get key string (separate endpoint; not included in create response).
	ksURL := fmt.Sprintf("%s/%s/keyString", apiKeysBaseURL, keyName)
	ksBody, err := doAPIKeysRequest(opCtx, client, http.MethodGet, ksURL, nil, "")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return apiKeyOperationTimeoutError(op.Name)
		}
		return err
	}

	var ks keyStringResult
	if err := json.Unmarshal(ksBody, &ks); err != nil {
		return fmt.Errorf("parse key string: %w", err)
	}

	// Add keyString to the full key resource for structured output.
	keyResp["keyString"] = ks.KeyString

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}
	defer closer()

	if keyOnly {
		_, err := fmt.Fprintln(w, ks.KeyString)
		return err
	}

	if outputFormat == "txtar" {
		fmt.Fprintf(os.Stderr, "WARNING: format %q not supported for create-api-key, falling back to text\n", outputFormat)
	}
	if outputFormat == "text" || outputFormat == "txtar" {
		fmt.Fprintf(w, "Name:          %s\n", keyName)
		if dn, ok := keyResp["displayName"].(string); ok && dn != "" {
			fmt.Fprintf(w, "Display Name:  %s\n", dn)
		}
		fmt.Fprintf(w, "API Key:       %s\n", ks.KeyString)
		if targets := extractAPITargets(keyResp); len(targets) > 0 {
			fmt.Fprintf(w, "Restricted to: %s\n", strings.Join(targets, ", "))
		}
		return nil
	}
	return writeFormatted(w, outputFormat, keyResp)
}

// extractAPITargets returns the service names from the restrictions.apiTargets
// field of a Key resource parsed as map[string]any.
func extractAPITargets(keyResp map[string]any) []string {
	restrictions, ok := keyResp["restrictions"].(map[string]any)
	if !ok {
		return nil
	}
	targets, ok := restrictions["apiTargets"].([]any)
	if !ok {
		return nil
	}
	var services []string
	for _, t := range targets {
		if tm, ok := t.(map[string]any); ok {
			if svc, ok := tm["service"].(string); ok {
				services = append(services, svc)
			}
		}
	}
	return services
}
