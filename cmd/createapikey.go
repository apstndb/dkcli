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
	"runtime"
	"strings"
	"time"

	dkapi "github.com/apstndb/developerknowledge-go"
	"github.com/spf13/cobra"
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

const createAPIKeyFileMode = 0o600

func ownerOnlyPermissionExample() string {
	return fmt.Sprintf("%04o", createAPIKeyFileMode)
}

func validateOwnerOnlyPermissions(file string, perms os.FileMode) error {
	if perms&0o077 != 0 {
		return fmt.Errorf("refusing to write secret to %q with permissions %04o; use owner-only permissions with owner write (for example %s)", file, perms, ownerOnlyPermissionExample())
	}
	if perms&0o200 == 0 {
		return fmt.Errorf("refusing to write secret to %q without owner write permission; use owner-only permissions with owner write (for example %s)", file, ownerOnlyPermissionExample())
	}
	return nil
}

func warnWindowsOutputACL(file string) {
	if runtime.GOOS == "windows" {
		fmt.Fprintf(os.Stderr, "WARNING: Windows does not enforce owner-only access via mode bits for %q; secret file access depends on the directory ACLs\n", file)
	}
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

func createAPIKeyOutWriter(file string) (io.Writer, func() error, error) {
	if file == "" {
		return os.Stdout, func() error { return nil }, nil
	}

	f, err := openExistingCreateAPIKeyFile(file)
	switch {
	case err == nil:
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			return nil, nil, fmt.Errorf("refusing to write secret to non-regular file %q", file)
		}
		if runtime.GOOS != "windows" {
			if err := validateOwnerOnlyPermissions(file, info.Mode().Perm()); err != nil {
				_ = f.Close()
				return nil, nil, err
			}
		}
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		warnWindowsOutputACL(file)
		return f, f.Close, nil
	case errors.Is(err, os.ErrNotExist):
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, createAPIKeyFileMode)
		if err != nil {
			return nil, nil, err
		}
		if err := f.Chmod(createAPIKeyFileMode); err != nil {
			_ = f.Close()
			_ = os.Remove(file)
			return nil, nil, err
		}
		warnWindowsOutputACL(file)
		return f, f.Close, nil
	default:
		if runtime.GOOS != "windows" && errors.Is(err, os.ErrPermission) {
			info, statErr := os.Lstat(file)
			if statErr == nil && info.Mode().IsRegular() {
				if err := validateOwnerOnlyPermissions(file, info.Mode().Perm()); err != nil {
					return nil, nil, err
				}
			}
		}
		return nil, nil, err
	}
}

func newAPIKeysClient(ctx context.Context) (*http.Client, error) {
	// runCreateAPIKey already wraps the whole API Keys LRO in a dedicated
	// operation context, so this client intentionally leaves Timeout unset to
	// avoid a second competing timeout source and keep timeout errors
	// consistent.
	// Charge quota/billing to the configured ADC quota project, matching the
	// rest of the CLI, rather than implicitly using the target project passed
	// to --project.
	return newADCHTTPClient(ctx, authRequireADC, 0, apiKeysBaseURL)
}

func doAPIKeysRequest(ctx context.Context, client *http.Client, method, url string, body []byte, contentType string) ([]byte, error) {
	// create-api-key talks to the API Keys API via a short, bounded sequence of
	// OAuth-authenticated requests, so it uses a dedicated helper instead of
	// the Developer Knowledge-specific apiClient rate-limit/retry path.
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
	return dkapi.CheckResponse(resp)
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
		if err := dkapi.SleepContext(opCtx, createAPIKeyPollInterval); err != nil {
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

	w, closer, err := createAPIKeyOutWriter(outputFile)
	if err != nil {
		return err
	}
	finishWrite := func(writeErr error) error {
		closeErr := closer()
		if writeErr != nil {
			if closeErr != nil {
				return errors.Join(writeErr, closeErr)
			}
			return writeErr
		}
		return closeErr
	}

	if keyOnly {
		_, err := fmt.Fprintln(w, ks.KeyString)
		return finishWrite(err)
	}

	if outputFormat == "txtar" {
		fmt.Fprintf(os.Stderr, "WARNING: format %q not supported for create-api-key, falling back to text\n", outputFormat)
	}
	if outputFormat == "text" || outputFormat == "txtar" {
		if _, err := fmt.Fprintf(w, "Name:          %s\n", keyName); err != nil {
			return finishWrite(err)
		}
		if dn, ok := keyResp["displayName"].(string); ok && dn != "" {
			if _, err := fmt.Fprintf(w, "Display Name:  %s\n", dn); err != nil {
				return finishWrite(err)
			}
		}
		if _, err := fmt.Fprintf(w, "API Key:       %s\n", ks.KeyString); err != nil {
			return finishWrite(err)
		}
		if targets := extractAPITargets(keyResp); len(targets) > 0 {
			if _, err := fmt.Fprintf(w, "Restricted to: %s\n", strings.Join(targets, ", ")); err != nil {
				return finishWrite(err)
			}
		}
		return finishWrite(nil)
	}
	return finishWrite(writeFormatted(w, outputFormat, keyResp))
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
