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

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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
	if client.client == nil {
		t.Fatal("expected HTTP client when using API key auth")
	}
	if client.client == http.DefaultClient {
		t.Fatal("expected a dedicated HTTP client when using API key auth")
	}
	if client.client.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %v, want %v", client.client.Timeout, defaultHTTPTimeout)
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
	if client.client.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %v, want %v", client.client.Timeout, defaultHTTPTimeout)
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

func TestAPIClient_DoRequestHonorsCanceledContextWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	client := &apiClient{
		client: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				return nil, nil
			}),
		},
		ctx:     ctx,
		limiter: rate.NewLimiter(rate.Every(time.Hour), 1),
	}
	client.limiter.Allow()

	_, err := client.doGet("https://example.com")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("request should not be attempted after context cancellation")
	}
}

func TestAPIClient_DoRequestUsesRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	client := &apiClient{
		client: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				<-req.Context().Done()
				return nil, req.Context().Err()
			}),
		},
		ctx:     ctx,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	go func() {
		_, err := client.doGet("https://example.com")
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("request did not observe context cancellation")
	}
}

func TestAPIClient_DoRequestCancelsRetryBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attempts := 0
	client := &apiClient{
		client: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				cancel()
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Retry-After": []string{"60"}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}),
		},
		ctx:     ctx,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	_, err := client.doGet("https://example.com")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestYAMLTagsUseSnakeCase(t *testing.T) {
	got, err := yaml.Marshal(map[string]any{
		"document": Document{
			DataSource: "example.com",
			UpdateTime: "2026-04-18T00:00:00Z",
		},
		"search": searchResponse{
			NextPageToken: "next-token",
		},
		"answer": answerQueryResponse{
			Answer: Answer{AnswerText: "grounded"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	text := string(got)
	for _, want := range []string{"data_source:", "update_time:", "next_page_token:", "answer_text:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("yaml output %q does not contain %q", text, want)
		}
	}
	for _, unwanted := range []string{"dataSource:", "updateTime:", "nextPageToken:", "answerText:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("yaml output %q unexpectedly contains %q", text, unwanted)
		}
	}
}
