package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	dkapi "github.com/apstndb/developerknowledge-go"
)

func TestFetchSearchPage(t *testing.T) {
	t.Parallel()

	want := &searchResponse{
		Results: []DocumentChunk{
			{Parent: "documents/example.com/page", ID: "c1", Content: "content A", Document: &Document{Title: "Page A", DataSource: "docs.cloud.google.com"}},
			{Parent: "documents/example.com/other", ID: "c2", Content: "content B"},
		},
		NextPageToken: "next-token",
	}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/documents:searchDocumentChunks" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("api key = %q, want %q", got, "test-key")
		}
		if got := r.URL.Query().Get("query"); got != "test query" {
			t.Errorf("query = %q, want %q", got, "test query")
		}
		json.NewEncoder(w).Encode(want)
	}))

	got, err := client.fetchSearchPage("test query", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(got.Results))
	}
	if got.Results[0].Parent != "documents/example.com/page" {
		t.Errorf("results[0].Parent = %q, want %q", got.Results[0].Parent, "documents/example.com/page")
	}
	if got.NextPageToken != "next-token" {
		t.Errorf("nextPageToken = %q, want %q", got.NextPageToken, "next-token")
	}
	if got.Results[0].Document == nil || got.Results[0].Document.Title != "Page A" {
		t.Fatalf("expected embedded document metadata, got %+v", got.Results[0].Document)
	}
}

func TestFetchSearchPage_WithPageToken(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("pageToken"); got != "abc123" {
			t.Errorf("pageToken = %q, want %q", got, "abc123")
		}
		json.NewEncoder(w).Encode(&searchResponse{})
	}))

	_, err := client.fetchSearchPage("test", 0, "abc123", "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchSearchPage_WithPageSize(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("pageSize"); got != "10" {
			t.Errorf("pageSize = %q, want %q", got, "10")
		}
		json.NewEncoder(w).Encode(&searchResponse{})
	}))

	_, err := client.fetchSearchPage("test", 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchSearchPage_WithFilter(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("filter"); got != `data_source="docs.cloud.google.com"` {
			t.Errorf("filter = %q, want %q", got, `data_source="docs.cloud.google.com"`)
		}
		json.NewEncoder(w).Encode(&searchResponse{})
	}))

	_, err := client.fetchSearchPage("test", 0, "", `data_source="docs.cloud.google.com"`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchSearchPage_APIError(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    400,
				"message": "invalid query",
				"status":  "INVALID_ARGUMENT",
			},
		})
	}))

	_, err := client.fetchSearchPage("", 0, "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ae *dkapi.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *dkapi.APIError, got %T: %v", err, err)
	}
	if ae.Code != 400 {
		t.Errorf("code = %d, want 400", ae.Code)
	}
}

func TestValidateSearchFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int
		pages   int
		wantErr string
	}{
		{name: "defaults", size: 0, pages: 5},
		{name: "max_page_size", size: 20, pages: 0},
		{name: "negative_page_size", size: -1, pages: 5, wantErr: "--page-size"},
		{name: "too_large_page_size", size: 21, pages: 5, wantErr: "--page-size"},
		{name: "negative_max_pages", size: 10, pages: -1, wantErr: "--max-pages"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSearchFlags(tt.size, tt.pages)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSearchFlags(%d, %d) = %v, want nil", tt.size, tt.pages, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSearchFlags(%d, %d) = nil, want error containing %q", tt.size, tt.pages, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}
