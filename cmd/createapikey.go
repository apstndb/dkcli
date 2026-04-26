package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	projectID   string
	displayName string
	keyOnly     bool
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

const createAPIKeyFileMode = 0o600

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
			perms := info.Mode().Perm()
			if perms&0o077 != 0 {
				_ = f.Close()
				return nil, nil, fmt.Errorf("refusing to write secret to %q with permissions %04o; use owner-only permissions with owner write (for example 0600)", file, perms)
			}
			if perms&0o200 == 0 {
				_ = f.Close()
				return nil, nil, fmt.Errorf("refusing to write secret to %q without owner write permission; use owner-only permissions with owner write (for example 0600)", file)
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
		return f, f.Close, nil
	case errors.Is(err, os.ErrNotExist):
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, createAPIKeyFileMode)
		if err != nil {
			return nil, nil, err
		}
		if err := f.Chmod(createAPIKeyFileMode); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		return f, f.Close, nil
	default:
		if runtime.GOOS != "windows" && errors.Is(err, os.ErrPermission) {
			info, statErr := os.Lstat(file)
			if statErr == nil && info.Mode().IsRegular() {
				perms := info.Mode().Perm()
				if perms&0o077 == 0 && perms&0o200 == 0 {
					return nil, nil, fmt.Errorf("refusing to write secret to %q without owner write permission; use owner-only permissions with owner write (for example 0600)", file)
				}
			}
		}
		return nil, nil, err
	}
}

func runCreateAPIKey(cmd *cobra.Command, args []string) error {
	project := resolveProjectID()
	if project == "" {
		return fmt.Errorf("--project is required (or set GOOGLE_CLOUD_PROJECT)")
	}

	ctx := cmd.Context()

	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return fmt.Errorf("get credentials: %w (run 'gcloud auth application-default login')", err)
	}
	client := &http.Client{
		Transport: &quotaProjectTransport{
			Base:    &oauth2.Transport{Source: ts},
			Project: project,
		},
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

	createURL := fmt.Sprintf("https://apikeys.googleapis.com/v2/projects/%s/locations/global/keys", project)
	resp, err := client.Post(createURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	body, err := checkResponse(resp)
	if err != nil {
		return err
	}

	var op lroOperation
	if err := json.Unmarshal(body, &op); err != nil {
		return err
	}

	// Poll until done
	deadline := time.Now().Add(60 * time.Second)
	for !op.Done {
		if time.Now().After(deadline) {
			return fmt.Errorf("operation timed out: %s", op.Name)
		}
		time.Sleep(2 * time.Second)

		if verbose {
			fmt.Fprintf(os.Stderr, "Polling operation %s...\n", op.Name)
		}
		pollResp, err := client.Get("https://apikeys.googleapis.com/v2/" + op.Name)
		if err != nil {
			return err
		}
		pollBody, err := checkResponse(pollResp)
		if err != nil {
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
	ksResp, err := client.Get(fmt.Sprintf("https://apikeys.googleapis.com/v2/%s/keyString", keyName))
	if err != nil {
		return err
	}
	ksBody, err := checkResponse(ksResp)
	if err != nil {
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
