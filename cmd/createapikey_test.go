package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

func TestResolveProjectID(t *testing.T) {
	// Not parallel: modifies global projectID and env vars.
	orig := projectID
	t.Cleanup(func() { projectID = orig })

	projectID = "from-flag"
	if got := resolveProjectID(); got != "from-flag" {
		t.Errorf("got %q, want %q", got, "from-flag")
	}

	projectID = ""
	t.Setenv("GOOGLE_CLOUD_PROJECT", "from-env")
	if got := resolveProjectID(); got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}

	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "from-sdk")
	if got := resolveProjectID(); got != "from-sdk" {
		t.Errorf("got %q, want %q", got, "from-sdk")
	}

	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	if got := resolveProjectID(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractAPITargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		resp map[string]any
		want []string
	}{
		{
			name: "single_target",
			resp: map[string]any{
				"restrictions": map[string]any{
					"apiTargets": []any{
						map[string]any{"service": "developerknowledge.googleapis.com"},
					},
				},
			},
			want: []string{"developerknowledge.googleapis.com"},
		},
		{
			name: "multiple_targets",
			resp: map[string]any{
				"restrictions": map[string]any{
					"apiTargets": []any{
						map[string]any{"service": "serviceA.googleapis.com"},
						map[string]any{"service": "serviceB.googleapis.com"},
					},
				},
			},
			want: []string{"serviceA.googleapis.com", "serviceB.googleapis.com"},
		},
		{
			name: "no_restrictions",
			resp: map[string]any{},
			want: nil,
		},
		{
			name: "empty_targets",
			resp: map[string]any{
				"restrictions": map[string]any{},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAPITargets(tt.resp)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestQuotaProjectTransport(t *testing.T) {
	t.Parallel()

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-goog-user-project")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{
		Transport: &quotaProjectTransport{
			Base:    http.DefaultTransport,
			Project: "my-project",
		},
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotHeader != "my-project" {
		t.Errorf("x-goog-user-project = %q, want %q", gotHeader, "my-project")
	}
}

func TestNewAPIKeysClient_LeavesClientTimeoutUnset(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	orig := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
			t.Fatalf("scopes = %v, want [%q]", scopes, cloudPlatformScope)
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = orig })

	client, err := newAPIKeysClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != 0 {
		t.Fatalf("timeout = %v, want 0", client.Timeout)
	}
}

func TestNewAPIKeysClient_UsesResolvedQuotaProject(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	orig := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = orig })

	client, err := newAPIKeysClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var gotQuota string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuota = r.Header.Get("x-goog-user-project")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotQuota != "quota-project" {
		t.Fatalf("x-goog-user-project = %q, want %q", gotQuota, "quota-project")
	}
}

func TestNewAPIKeysClient_FailsWithoutQuotaProjectForUserADC(t *testing.T) {
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

	_, err = newAPIKeysClient(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "set-quota-project") {
		t.Fatalf("error = %q, want quota project guidance", err)
	}
}

func TestDoAPIKeysRequest_UsesRequestContext(t *testing.T) {
	type contextKey string

	ctx := context.WithValue(context.Background(), contextKey("test"), "value")
	called := false
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			if got := req.Context().Value(contextKey("test")); got != "value" {
				t.Fatalf("request context value = %v, want %q", got, "value")
			}
			return nil, context.Canceled
		}),
	}

	_, err := doAPIKeysRequest(ctx, client, http.MethodGet, "https://example.com", nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if !called {
		t.Fatal("expected request to reach transport")
	}
}

func TestRunCreateAPIKey_HonorsCancellationWhilePolling(t *testing.T) {
	origProjectID := projectID
	origDisplayName := displayName
	origKeyOnly := keyOnly
	origOutputFormat := outputFormat
	origOutputFile := outputFile
	origVerbose := verbose
	origBaseURL := apiKeysBaseURL
	origPollInterval := createAPIKeyPollInterval
	origRequestTimeout := createAPIKeyOperationTimeout
	t.Cleanup(func() {
		projectID = origProjectID
		displayName = origDisplayName
		keyOnly = origKeyOnly
		outputFormat = origOutputFormat
		outputFile = origOutputFile
		verbose = origVerbose
		apiKeysBaseURL = origBaseURL
		createAPIKeyPollInterval = origPollInterval
		createAPIKeyOperationTimeout = origRequestTimeout
	})

	projectID = "test-project"
	displayName = "dkcli"
	keyOnly = false
	outputFormat = "text"
	outputFile = ""
	verbose = false
	createAPIKeyPollInterval = time.Hour
	createAPIKeyOperationTimeout = 2 * time.Hour
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	origTokenSource := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = origTokenSource })

	created := make(chan struct{}, 1)
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/projects/test-project/locations/global/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"operations/test-op","done":false}`)
			select {
			case created <- struct{}{}:
			default:
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/test-op":
			polls++
			t.Fatalf("unexpected poll request after cancellation")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	apiKeysBaseURL = srv.URL + "/v2"

	ctx, cancel := context.WithCancel(context.Background())
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)

	done := make(chan error, 1)
	go func() {
		done <- runCreateAPIKey(cmd, nil)
	}()

	select {
	case <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for create request")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runCreateAPIKey did not exit after cancellation")
	}

	if polls != 0 {
		t.Fatalf("polls = %d, want 0", polls)
	}
}

func TestRunCreateAPIKey_OperationTimeoutIsHardCapped(t *testing.T) {
	origProjectID := projectID
	origDisplayName := displayName
	origKeyOnly := keyOnly
	origOutputFormat := outputFormat
	origOutputFile := outputFile
	origVerbose := verbose
	origBaseURL := apiKeysBaseURL
	origPollInterval := createAPIKeyPollInterval
	origTimeout := createAPIKeyOperationTimeout
	t.Cleanup(func() {
		projectID = origProjectID
		displayName = origDisplayName
		keyOnly = origKeyOnly
		outputFormat = origOutputFormat
		outputFile = origOutputFile
		verbose = origVerbose
		apiKeysBaseURL = origBaseURL
		createAPIKeyPollInterval = origPollInterval
		createAPIKeyOperationTimeout = origTimeout
	})

	projectID = "test-project"
	displayName = "dkcli"
	keyOnly = false
	outputFormat = "text"
	outputFile = ""
	verbose = false
	createAPIKeyPollInterval = 5 * time.Millisecond
	createAPIKeyOperationTimeout = 200 * time.Millisecond

	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	origTokenSource := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = origTokenSource })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/projects/test-project/locations/global/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"operations/test-op","done":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/operations/test-op":
			time.Sleep(100 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"operations/test-op","done":false}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	apiKeysBaseURL = srv.URL + "/v2"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runCreateAPIKey(cmd, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "operation timed out") {
		t.Fatalf("error = %q, want timeout", err)
	}
}

func TestRunCreateAPIKey_CreateRequestTimeoutUsesOperationTimeoutMessage(t *testing.T) {
	origProjectID := projectID
	origDisplayName := displayName
	origKeyOnly := keyOnly
	origOutputFormat := outputFormat
	origOutputFile := outputFile
	origVerbose := verbose
	origBaseURL := apiKeysBaseURL
	origPollInterval := createAPIKeyPollInterval
	origTimeout := createAPIKeyOperationTimeout
	t.Cleanup(func() {
		projectID = origProjectID
		displayName = origDisplayName
		keyOnly = origKeyOnly
		outputFormat = origOutputFormat
		outputFile = origOutputFile
		verbose = origVerbose
		apiKeysBaseURL = origBaseURL
		createAPIKeyPollInterval = origPollInterval
		createAPIKeyOperationTimeout = origTimeout
	})

	projectID = "test-project"
	displayName = "dkcli"
	keyOnly = false
	outputFormat = "text"
	outputFile = ""
	verbose = false
	createAPIKeyOperationTimeout = 200 * time.Millisecond
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "quota-project")

	origTokenSource := defaultTokenSource
	defaultTokenSource = func(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}), nil
	}
	t.Cleanup(func() { defaultTokenSource = origTokenSource })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/projects/test-project/locations/global/keys" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		time.Sleep(time.Second)
	}))
	t.Cleanup(srv.Close)
	apiKeysBaseURL = srv.URL + "/v2"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runCreateAPIKey(cmd, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "operation timed out" {
		t.Fatalf("error = %q, want %q", err.Error(), "operation timed out")
	}
}
