package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	projectID   string
	displayName string
)

var createAPIKeyCmd = &cobra.Command{
	Use:   "create-api-key",
	Short: "Create an API key restricted to Developer Knowledge API",
	RunE:  runCreateAPIKey,
}

func init() {
	createAPIKeyCmd.Flags().StringVarP(&projectID, "project", "p", "", "Google Cloud project ID (env: GOOGLE_CLOUD_PROJECT)")
	createAPIKeyCmd.Flags().StringVarP(&displayName, "display-name", "d", "dkcli", "display name for the API key")
	rootCmd.AddCommand(createAPIKeyCmd)
}

type createKeyRequest struct {
	DisplayName  string            `json:"displayName"`
	Restrictions keyRestrictions   `json:"restrictions"`
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

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}
	defer closer()

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

type quotaProjectTransport struct {
	Base    http.RoundTripper
	Project string
}

func (t *quotaProjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("x-goog-user-project", t.Project)
	return t.Base.RoundTrip(req)
}
