package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/time/rate"
)

// newTestClient creates an apiClient pointing at an httptest server.
func newTestClient(t *testing.T, handler http.Handler) *apiClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &apiClient{
		baseURL: srv.URL + "/v1alpha",
		apiKey:  "test-key",
		client:  srv.Client(),
		limiter: rate.NewLimiter(rate.Inf, 0),
		verbose: false,
	}
}

func TestFormatExtension(t *testing.T) {
	t.Parallel()
	tests := []struct {
		format string
		want   string
	}{
		{"json", ".json"},
		{"jsonl", ".jsonl"},
		{"yaml", ".yaml"},
		{"txtar", ".txtar"},
		{"text", ".md"},
		{"", ".md"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			got := formatExtension(tt.format)
			if got != tt.want {
				t.Errorf("formatExtension(%q) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

func TestDocFilePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dir, docName, format string
		want                 string
	}{
		{"/out", "documents/example.com/page", "text", filepath.Join("/out", "example.com/page.md")},
		{"/out", "documents/example.com/page", "json", filepath.Join("/out", "example.com/page.json")},
		{"/out", "example.com/page", "yaml", filepath.Join("/out", "example.com/page.yaml")},
	}
	for _, tt := range tests {
		t.Run(tt.docName+"_"+tt.format, func(t *testing.T) {
			t.Parallel()
			got := docFilePath(tt.dir, tt.docName, tt.format)
			if got != tt.want {
				t.Errorf("docFilePath(%q, %q, %q) = %q, want %q", tt.dir, tt.docName, tt.format, got, tt.want)
			}
		})
	}
}

func TestFormatDocForFile(t *testing.T) {
	t.Parallel()
	doc := &Document{
		Name:    "documents/example.com/page",
		URI:     "https://example.com/page",
		Content: "Hello world",
	}

	t.Run("text", func(t *testing.T) {
		t.Parallel()
		got, err := formatDocForFile(doc, "text", false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "Hello world\n" {
			t.Errorf("got %q, want %q", got, "Hello world\n")
		}
	})

	t.Run("text_frontmatter", func(t *testing.T) {
		t.Parallel()
		got, err := formatDocForFile(doc, "text", true)
		if err != nil {
			t.Fatal(err)
		}
		s := string(got)
		if s[:4] != "---\n" {
			t.Errorf("expected frontmatter start, got %q", s[:4])
		}
		if len(s) < 10 {
			t.Errorf("frontmatter output too short: %q", s)
		}
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()
		got, err := formatDocForFile(doc, "json", false)
		if err != nil {
			t.Fatal(err)
		}
		var parsed Document
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed.Name != doc.Name {
			t.Errorf("name = %q, want %q", parsed.Name, doc.Name)
		}
	})

	t.Run("txtar", func(t *testing.T) {
		t.Parallel()
		got, err := formatDocForFile(doc, "txtar", false)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("-- %s --\n%s\n", doc.Name, doc.Content)
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestFetchBatchGet(t *testing.T) {
	t.Parallel()

	docs := []Document{
		{Name: "documents/example.com/a", Content: "AAA"},
		{Name: "documents/example.com/b", Content: "BBB"},
	}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha/documents:batchGet" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("api key = %q, want %q", got, "test-key")
		}
		names := r.URL.Query()["names"]
		if len(names) != 2 {
			t.Errorf("expected 2 names, got %d", len(names))
		}
		resp := batchGetResponse{Documents: docs}
		json.NewEncoder(w).Encode(resp)
	}))

	got, err := client.fetchBatchGet([]string{"documents/example.com/a", "documents/example.com/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d documents, want 2", len(got))
	}
	if got[0].Content != "AAA" {
		t.Errorf("doc[0].Content = %q, want %q", got[0].Content, "AAA")
	}
	if got[1].Content != "BBB" {
		t.Errorf("doc[1].Content = %q, want %q", got[1].Content, "BBB")
	}
}

func TestFetchBatchGet_APIError(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    400,
				"message": "invalid document name",
				"status":  "INVALID_ARGUMENT",
			},
		})
	}))

	_, err := client.fetchBatchGet([]string{"bad-name"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apiError, got %T: %v", err, err)
	}
	if ae.Code != 400 {
		t.Errorf("error code = %d, want 400", ae.Code)
	}
}

func TestFetchBatchBisect(t *testing.T) {
	t.Parallel()

	// Simulate: batch containing "bad" fails with 400, others succeed.
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		names := r.URL.Query()["names"]
		for _, n := range names {
			if n == "documents/bad" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    400,
						"message": "not found",
						"status":  "NOT_FOUND",
					},
				})
				return
			}
		}
		var docs []Document
		for _, n := range names {
			docs = append(docs, Document{Name: n, Content: "content"})
		}
		json.NewEncoder(w).Encode(batchGetResponse{Documents: docs})
	}))

	docs, docErrs, fatal := client.fetchBatchBisect([]string{
		"documents/a", "documents/bad", "documents/b",
	})
	if fatal != nil {
		t.Fatalf("unexpected fatal error: %v", fatal)
	}
	if len(docs) != 2 {
		t.Errorf("got %d docs, want 2", len(docs))
	}
	if len(docErrs) != 1 {
		t.Fatalf("got %d doc errors, want 1", len(docErrs))
	}
	if docErrs[0] == nil {
		t.Fatal("expected non-nil doc error")
	}
}

func TestFetchBatchBisect_NonBisectable(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))

	docs, docErrs, fatal := client.fetchBatchBisect([]string{"documents/a"})
	if fatal == nil {
		t.Fatal("expected fatal error for 500")
	}
	if docs != nil {
		t.Errorf("expected nil docs, got %v", docs)
	}
	if docErrs != nil {
		t.Errorf("expected nil docErrs, got %v", docErrs)
	}
}

func TestWriteBatchOutdir(t *testing.T) {
	t.Parallel()

	docs := []Document{
		{Name: "documents/example.com/page1", Content: "Page 1 content"},
		{Name: "documents/example.com/sub/page2", Content: "Page 2 content"},
	}

	dir := t.TempDir()
	err := writeBatchOutdir(docs, dir, "text", false)
	if err != nil {
		t.Fatal(err)
	}

	for _, doc := range docs {
		path := docFilePath(dir, doc.Name, "text")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read %s: %v", path, err)
		}
		if string(data) != doc.Content+"\n" {
			t.Errorf("file %s: got %q, want %q", path, data, doc.Content+"\n")
		}
	}
}

func TestWriteBatchOutdir_JSONFormat(t *testing.T) {
	t.Parallel()

	docs := []Document{
		{Name: "documents/example.com/page", URI: "https://example.com/page", Content: "Hello"},
	}

	dir := t.TempDir()
	err := writeBatchOutdir(docs, dir, "json", false)
	if err != nil {
		t.Fatal(err)
	}

	path := docFilePath(dir, docs[0].Name, "json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	var parsed Document
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON in %s: %v", path, err)
	}
	if parsed.Name != docs[0].Name {
		t.Errorf("name = %q, want %q", parsed.Name, docs[0].Name)
	}
	if parsed.Content != "Hello" {
		t.Errorf("content = %q, want %q", parsed.Content, "Hello")
	}
}
