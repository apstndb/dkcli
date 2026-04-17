package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestNewAPIClient_PrefersAPIKey(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "api-key")

	orig := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		t.Fatal("defaultTokenSource should not be called when API key is set")
		return nil, nil
	}
	t.Cleanup(func() { defaultTokenSource = orig })

	client, err := newAPIClient(context.Background(), authPreferAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if client.apiKey != "api-key" {
		t.Fatalf("apiKey = %q, want %q", client.apiKey, "api-key")
	}
	if client.client != http.DefaultClient {
		t.Fatal("expected default HTTP client when using API key auth")
	}
}

func TestNewAPIClient_FallsBackToADC(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	orig := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
			t.Fatalf("scopes = %v, want [%q]", scopes, cloudPlatformScope)
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = orig })

	client, err := newAPIClient(context.Background(), authPreferAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if client.apiKey != "" {
		t.Fatalf("apiKey = %q, want empty", client.apiKey)
	}
	if client.client == http.DefaultClient {
		t.Fatal("expected OAuth HTTP client when falling back to ADC")
	}
}

func TestNewAPIClient_SetsQuotaProjectHeader(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	orig := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = orig })

	var gotQuota string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuota = r.Header.Get("x-goog-user-project")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := newAPIClient(context.Background(), authPreferAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotQuota != "quota-project" {
		t.Fatalf("x-goog-user-project = %q, want %q", gotQuota, "quota-project")
	}
}

func TestResolveQuotaProjectID(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")
	got, _ := resolveQuotaProjectID()
	if got != "quota-project" {
		t.Fatalf("got %q, want %q", got, "quota-project")
	}
}

func TestResolveQuotaProjectIDFromADCFile(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "adc.json")
	data, err := json.Marshal(map[string]string{"quota_project_id": "from-file"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	orig := adcCredentialsPath
	adcCredentialsPath = func() string { return path }
	t.Cleanup(func() { adcCredentialsPath = orig })

	got, _ := resolveQuotaProjectID()
	if got != "from-file" {
		t.Fatalf("got %q, want %q", got, "from-file")
	}
}

func TestNewAPIClient_FailsFastWithoutQuotaProjectForUserADC(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "adc.json")
	data, err := json.Marshal(map[string]string{"type": "authorized_user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	origPath := adcCredentialsPath
	adcCredentialsPath = func() string { return path }
	t.Cleanup(func() { adcCredentialsPath = origPath })

	origTokenSource := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = origTokenSource })

	_, err = newAPIClient(context.Background(), authPreferAPIKey)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "set-quota-project") {
		t.Fatalf("error = %q, want quota project guidance", err)
	}
}
