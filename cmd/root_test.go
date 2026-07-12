package cmd

import (
	"bytes"
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

	dkapi "github.com/apstndb/developerknowledge-go"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestValidateOutputFormat(t *testing.T) {
	for _, format := range []string{"text", "json", "jsonl", "yaml", "txtar"} {
		t.Run(format, func(t *testing.T) {
			if err := validateOutputFormat(format); err != nil {
				t.Fatalf("validateOutputFormat(%q) = %v, want nil", format, err)
			}
		})
	}

	err := validateOutputFormat("xml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown format: xml") {
		t.Fatalf("error = %q, want unknown format message", err)
	}
}

func TestFinishOutputReturnsCloseError(t *testing.T) {
	closeErr := errors.New("close failed")

	err := finishOutput(nil, func() error { return closeErr })
	if !errors.Is(err, closeErr) {
		t.Fatalf("finishOutput(nil, closeErr) = %v, want closeErr", err)
	}
}

func TestFinishOutputJoinsWriteAndCloseErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")

	err := finishOutput(writeErr, func() error { return closeErr })
	if !errors.Is(err, writeErr) {
		t.Fatalf("finishOutput error = %v, want writeErr", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("finishOutput error = %v, want closeErr", err)
	}
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
		if len(scopes) != 1 || scopes[0] != dkapi.CloudPlatformScope {
			t.Fatalf("scopes = %v, want [%q]", scopes, dkapi.CloudPlatformScope)
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

	client, err := newAPIClientForBaseURL(context.Background(), authPreferAPIKey, srv.URL)
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

func TestNewAPIClient_FailsFastWithoutQuotaProjectForUserADC(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "adc.json")
	data, err := json.Marshal(map[string]string{
		"type":          "authorized_user",
		"client_id":     "test-client-id",
		"client_secret": "test-client-secret",
		"refresh_token": "test-refresh-token",
		"token_uri":     "https://oauth2.googleapis.com/token",
	})
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
	defaultTokenSource = nil
	t.Cleanup(func() { defaultTokenSource = origTokenSource })

	_, err = newAPIClient(context.Background(), authPreferAPIKey)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "set-quota-project") {
		t.Fatalf("error = %q, want quota project guidance", err)
	}
}

func TestNewAPIClient_ADCFetchUsesQuotaProjectAndAllowedOrigin(t *testing.T) {
	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")

	var tokenMethod, authorization, quotaProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenMethod = r.Method
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		case "/v1/documents/example.com/page":
			authorization = r.Header.Get("Authorization")
			quotaProject = r.Header.Get("x-goog-user-project")
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "adc.json")
	data, err := json.Marshal(map[string]string{
		"type":             "authorized_user",
		"client_id":        "test-client-id",
		"client_secret":    "test-client-secret",
		"refresh_token":    "test-refresh-token",
		"token_uri":        srv.URL + "/token",
		"quota_project_id": "quota-project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	origPath := adcCredentialsPath
	adcCredentialsPath = func() string { return path }
	t.Cleanup(func() { adcCredentialsPath = origPath })

	origTokenSource := defaultTokenSource
	defaultTokenSource = nil
	t.Cleanup(func() { defaultTokenSource = origTokenSource })

	client, err := newAPIClientForBaseURL(context.Background(), authPreferAPIKey, srv.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.fetchDocument("documents/example.com/page"); err != nil {
		t.Fatal(err)
	}
	if tokenMethod != http.MethodPost {
		t.Fatalf("token request method = %s, want POST", tokenMethod)
	}
	if authorization != "Bearer test-token" {
		t.Fatalf("authorization = %q, want bearer token", authorization)
	}
	if quotaProject != "quota-project" {
		t.Fatalf("x-goog-user-project = %q, want %q", quotaProject, "quota-project")
	}
}

func TestAPIClient_DoRequestHonorsCanceledContextWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	client := &apiClient{
		baseURL: "https://example.com",
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
		baseURL: "https://example.com",
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
		baseURL: "https://example.com",
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

func TestDocContentLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "empty", content: "", want: 0},
		{name: "one_line_no_newline", content: "hello", want: 1},
		{name: "one_line_with_newline", content: "hello\n", want: 1},
		{name: "two_lines_no_trailing_newline", content: "hello\nworld", want: 2},
		{name: "two_lines_with_trailing_newline", content: "hello\nworld\n", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := docContentLines(tt.content); got != tt.want {
				t.Fatalf("docContentLines(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}

func TestPrintDocsSummaryLineCounts(t *testing.T) {
	var buf bytes.Buffer
	printDocsSummary(&buf, []Document{
		{Name: "documents/example.com/a", Content: "one line"},
		{Name: "documents/example.com/b", Content: "two\nlines\n"},
	})

	got := buf.String()
	for _, want := range []string{
		"documents/example.com/a (8 bytes, 1 lines)",
		"documents/example.com/b (10 bytes, 2 lines)",
		"total: 2 documents, 18 bytes, 3 lines",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q does not contain %q", got, want)
		}
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
			Answer: Answer{
				AnswerText: "grounded",
				Citations: []AnswerCitation{{
					StartIndex: 0,
					EndIndex:   8,
					Sources:    []CitationSource{{ReferenceIndex: 0}},
				}},
				References: []AnswerReference{{
					DocumentReference: &DocumentReference{
						DocumentChunk: &DocumentChunk{
							Parent: "documents/example.com/a",
							ID:     "chunk-1",
							Document: &Document{
								DataSource: "example.com",
								UpdateTime: "2026-04-18T00:00:00Z",
							},
						},
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	text := string(got)
	for _, want := range []string{
		"data_source:",
		"update_time:",
		"next_page_token:",
		"answer_text:",
		"start_index:",
		"end_index:",
		"reference_index:",
		"document_reference:",
		"document_chunk:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("yaml output %q does not contain %q", text, want)
		}
	}
	for _, unwanted := range []string{
		"dataSource:",
		"updateTime:",
		"nextPageToken:",
		"answerText:",
		"startIndex:",
		"endIndex:",
		"referenceIndex:",
		"documentReference:",
		"documentChunk:",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("yaml output %q unexpectedly contains %q", text, unwanted)
		}
	}
}
