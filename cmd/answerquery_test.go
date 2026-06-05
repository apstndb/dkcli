package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

func TestAnswerQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha:answerQuery" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}

		var req answerQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Query != "what is dkcli?" {
			t.Fatalf("query = %q, want %q", req.Query, "what is dkcli?")
		}

		json.NewEncoder(w).Encode(answerQueryResponse{
			Answer: Answer{
				AnswerText: "dkcli is a CLI for Developer Knowledge.",
				Citations: []AnswerCitation{{
					StartIndex: 0,
					EndIndex:   5,
					Sources:    []CitationSource{{ReferenceIndex: 0}},
				}},
				References: []AnswerReference{{
					DocumentReference: &DocumentReference{
						DocumentChunk: &DocumentChunk{
							Parent: "documents/developers.google.com/knowledge/api",
							ID:     "chunk-1",
							Document: &Document{
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
		baseURL: srv.URL + "/v1alpha",
		client:  srv.Client(),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	resp, err := client.answerQuery("what is dkcli?")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answer.AnswerText != "dkcli is a CLI for Developer Knowledge." {
		t.Fatalf("answer = %q", resp.Answer.AnswerText)
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

func TestRunAnswerQueryTextIncludesReferences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha:answerQuery" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(answerQueryResponse{
			Answer: Answer{
				AnswerText: "dkcli is a CLI for Developer Knowledge.",
				References: []AnswerReference{{
					DocumentReference: &DocumentReference{
						DocumentChunk: &DocumentChunk{
							Parent: "documents/developers.google.com/knowledge/api",
							ID:     "chunk-1",
							Document: &Document{
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

	origBaseURL := answerQueryBaseURL
	origLimiter := apiLimiter
	origOutputFormat := outputFormat
	origOutputFile := outputFile
	t.Cleanup(func() {
		answerQueryBaseURL = origBaseURL
		apiLimiter = origLimiter
		outputFormat = origOutputFormat
		outputFile = origOutputFile
	})

	answerQueryBaseURL = srv.URL + "/v1alpha"
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

func TestFormatAnswerTextNil(t *testing.T) {
	if got := formatAnswerText(nil); got != "" {
		t.Fatalf("formatAnswerText(nil) = %q, want empty", got)
	}
}

func TestFormatAnswerTextEmpty(t *testing.T) {
	if got := formatAnswerText(&Answer{}); got != "" {
		t.Fatalf("formatAnswerText(empty) = %q, want empty", got)
	}
}

func TestFormatAnswerTextReferenceFallback(t *testing.T) {
	got := formatAnswerText(&Answer{
		References: []AnswerReference{{
			DocumentReference: &DocumentReference{
				DocumentChunk: &DocumentChunk{},
			},
		}},
	})
	want := "References:\n[1] Untitled\n"
	if got != want {
		t.Fatalf("formatAnswerText(reference only) = %q, want %q", got, want)
	}
}
