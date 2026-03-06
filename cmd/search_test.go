package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestFetchSearchPage(t *testing.T) {
	t.Parallel()

	want := &searchResponse{
		Results: []DocumentChunk{
			{Parent: "documents/example.com/page", ID: "c1", Content: "content A"},
			{Parent: "documents/example.com/other", ID: "c2", Content: "content B"},
		},
		NextPageToken: "next-token",
	}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha/documents:searchDocumentChunks" {
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

	got, err := client.fetchSearchPage("test query", "")
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
}

func TestFetchSearchPage_WithPageToken(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("pageToken"); got != "abc123" {
			t.Errorf("pageToken = %q, want %q", got, "abc123")
		}
		json.NewEncoder(w).Encode(&searchResponse{})
	}))

	_, err := client.fetchSearchPage("test", "abc123")
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

	_, err := client.fetchSearchPage("", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apiError, got %T: %v", err, err)
	}
	if ae.Code != 400 {
		t.Errorf("code = %d, want 400", ae.Code)
	}
}
