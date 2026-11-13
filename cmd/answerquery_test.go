package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dkapi "github.com/apstndb/developerknowledge-go"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

func TestAnswerQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1:answerQuery" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}

		var req dkapi.AnswerQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Query != "what is dkcli?" {
			t.Fatalf("query = %q, want %q", req.Query, "what is dkcli?")
		}
		if req.Filter != "" {
			t.Fatalf("filter = %q, want empty", req.Filter)
		}

		json.NewEncoder(w).Encode(dkapi.AnswerQueryResponse{
			Answer: &dkapi.Answer{
				AnswerText: "dkcli is a CLI for Developer Knowledge.",
				Citations: []dkapi.AnswerCitation{{
					StartIndex: 0,
					EndIndex:   5,
					Sources:    []dkapi.CitationSource{{ReferenceIndex: 0}},
				}},
				References: []dkapi.AnswerReference{{
					DocumentReference: &dkapi.DocumentReference{
						DocumentChunk: &dkapi.DocumentChunk{
							Parent: "documents/developers.google.com/knowledge/api",
							ID:     "chunk-1",
							Document: &dkapi.Document{
								Title: "Developer Knowledge API",
								URI:   "https://developers.google.com/knowledge/api",
							},
						},
					},
				}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := &apiClient{
		baseURL: srv.URL + "/v1",
		client:  srv.Client(),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	resp, err := client.answerQuery("what is dkcli?", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answer == nil || resp.Answer.AnswerText != "dkcli is a CLI for Developer Knowledge." {
		t.Fatalf("answer = %+v", resp.Answer)
	}
	if len(resp.Answer.Citations) != 1 {
		t.Fatalf("citations = %d, want 1", len(resp.Answer.Citations))
	}
	if got := resp.Answer.Citations[0].Sources[0].ReferenceIndex; got != 0 {
		t.Fatalf("reference index = %d, want 0", got)
	}
	if len(resp.Answer.References) != 1 {
		t.Fatalf("references = %d, want 1", len(resp.Answer.References))
	}
	ref := resp.Answer.References[0].DocumentReference
	if ref == nil || ref.DocumentChunk == nil || ref.DocumentChunk.Document == nil {
		t.Fatal("expected document reference with document chunk metadata")
	}
	if got := ref.DocumentChunk.Document.URI; got != "https://developers.google.com/knowledge/api" {
		t.Fatalf("reference URI = %q", got)
	}
}

func TestAnswerQueryFilterSetAndOmitted(t *testing.T) {
	const wantFilter = `content_length_bytes < 50000 AND data_source = "docs.cloud.google.com"`
	var sawFilter, sawOmitted bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var req dkapi.AnswerQueryRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("decode: %v", err)
		}
		switch req.Query {
		case "filtered":
			sawFilter = true
			if req.Filter != wantFilter {
				t.Errorf("filter = %q, want %q", req.Filter, wantFilter)
			}
			if !strings.Contains(string(raw), "content_length_bytes") || !strings.Contains(string(raw), "data_source") {
				t.Errorf("body = %s, want snake_case filter fields", raw)
			}
		case "unfiltered":
			sawOmitted = true
			if req.Filter != "" {
				t.Errorf("filter = %q, want empty", req.Filter)
			}
			if strings.Contains(string(raw), `"filter"`) {
				t.Errorf("body = %s, want filter omitted", raw)
			}
		default:
			t.Errorf("query = %q", req.Query)
		}
		json.NewEncoder(w).Encode(dkapi.AnswerQueryResponse{
			Answer: &dkapi.Answer{AnswerText: "ok\n"},
		})
	}))
	t.Cleanup(srv.Close)

	client := &apiClient{
		baseURL: srv.URL + "/v1",
		client:  srv.Client(),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	if _, err := client.answerQuery("filtered", wantFilter); err != nil {
		t.Fatal(err)
	}
	if _, err := client.answerQuery("unfiltered", ""); err != nil {
		t.Fatal(err)
	}
	if !sawFilter || !sawOmitted {
		t.Fatalf("saw filter=%v omitted=%v", sawFilter, sawOmitted)
	}
}

func TestRunAnswerQuerySendsFlagVerbatim(t *testing.T) {
	const wantFilter = `update_time >= "2025-01-22T00:00:00Z"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var req dkapi.AnswerQueryRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Filter != wantFilter {
			t.Errorf("filter = %q, want %q", req.Filter, wantFilter)
		}
		json.NewEncoder(w).Encode(dkapi.AnswerQueryResponse{
			Answer: &dkapi.Answer{AnswerText: "ok\n"},
		})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "test-key")
	t.Setenv("GOOGLE_API_KEY", "")
	restoreAnswerQueryGlobals(t)
	answerQueryBaseURL = srv.URL + "/v1"
	apiLimiter = rate.NewLimiter(rate.Inf, 1)
	outputFormat = "text"
	answerQueryFilter = wantFilter

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runAnswerQuery(cmd, []string{"when", "updated"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunAnswerQueryJSONRetainsRelevanceScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "answer": {
    "answerText": "See the chunk.",
    "citations": [{"startIndex": 0, "endIndex": 3, "sources": [{"referenceIndex": 0}]}],
    "references": [{
      "documentReference": {
        "documentChunk": {
          "parent": "documents/docs.cloud.google.com/storage",
          "id": "chunk-1",
          "content": "body",
          "relevanceScore": 0.875
        }
      }
    }]
  }
}`)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "test-key")
	t.Setenv("GOOGLE_API_KEY", "")
	restoreAnswerQueryGlobals(t)
	answerQueryBaseURL = srv.URL + "/v1"
	apiLimiter = rate.NewLimiter(rate.Inf, 1)
	outputFormat = "json"
	outputFile = filepath.Join(t.TempDir(), "answer.json")
	answerQueryFilter = ""

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runAnswerQuery(cmd, []string{"score"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	var resp dkapi.AnswerQueryResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode output %s: %v", data, err)
	}
	chunk := resp.Answer.References[0].DocumentReference.DocumentChunk
	if chunk.RelevanceScore != 0.875 {
		t.Fatalf("relevanceScore = %v, want 0.875", chunk.RelevanceScore)
	}
	if !strings.Contains(string(data), `"relevanceScore": 0.875`) && !strings.Contains(string(data), `"relevanceScore":0.875`) {
		t.Fatalf("output = %s, want relevanceScore", data)
	}
}

func TestRunAnswerQueryTextIncludesReferences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1:answerQuery" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(dkapi.AnswerQueryResponse{
			Answer: &dkapi.Answer{
				AnswerText: "dkcli is a CLI for Developer Knowledge.",
				References: []dkapi.AnswerReference{{
					DocumentReference: &dkapi.DocumentReference{
						DocumentChunk: &dkapi.DocumentChunk{
							Parent: "documents/developers.google.com/knowledge/api",
							ID:     "chunk-1",
							Document: &dkapi.Document{
								Title: "Developer Knowledge API",
								URI:   "https://developers.google.com/knowledge/api",
							},
						},
					},
				}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "test-key")
	t.Setenv("GOOGLE_API_KEY", "")
	restoreAnswerQueryGlobals(t)
	answerQueryBaseURL = srv.URL + "/v1"
	apiLimiter = rate.NewLimiter(rate.Inf, 1)
	outputFormat = "text"
	outputFile = filepath.Join(t.TempDir(), "answer.txt")

	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetErr(&stderr)

	if err := runAnswerQuery(cmd, []string{"what", "is", "dkcli?"}); err != nil {
		t.Fatal(err)
	}

	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dkcli is a CLI for Developer Knowledge.\n",
		"\nReferences:\n",
		"[1] Developer Knowledge API - https://developers.google.com/knowledge/api\n",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("output = %q, want to contain %q", data, want)
		}
	}
}

func restoreAnswerQueryGlobals(t *testing.T) {
	t.Helper()
	origBaseURL := answerQueryBaseURL
	origLimiter := apiLimiter
	origOutputFormat := outputFormat
	origOutputFile := outputFile
	origFilter := answerQueryFilter
	t.Cleanup(func() {
		answerQueryBaseURL = origBaseURL
		apiLimiter = origLimiter
		outputFormat = origOutputFormat
		outputFile = origOutputFile
		answerQueryFilter = origFilter
	})
}

func TestFormatAnswerTextNil(t *testing.T) {
	if got := formatAnswerText(nil); got != "" {
		t.Fatalf("formatAnswerText(nil) = %q, want empty", got)
	}
}

func TestFormatAnswerTextEmpty(t *testing.T) {
	if got := formatAnswerText(&dkapi.Answer{}); got != "" {
		t.Fatalf("formatAnswerText(empty) = %q, want empty", got)
	}
}

func TestFormatAnswerTextReferenceFallback(t *testing.T) {
	got := formatAnswerText(&dkapi.Answer{
		References: []dkapi.AnswerReference{{
			DocumentReference: &dkapi.DocumentReference{
				DocumentChunk: &dkapi.DocumentChunk{},
			},
		}},
	})
	want := "References:\n[1] Untitled\n"
	if got != want {
		t.Fatalf("formatAnswerText(reference only) = %q, want %q", got, want)
	}
}
