package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

func TestNormalizeDocName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"documents/example.com/page", "documents/example.com/page"},
		{"example.com/page", "documents/example.com/page"},
		{"https://example.com/page", "documents/example.com/page"},
		{"http://example.com/page", "documents/example.com/page"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeDocName(tt.input)
			if got != tt.want {
				t.Errorf("normalizeDocName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatDocText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"with_newline", "hello\n", "hello\n"},
		{"without_newline", "hello", "hello\n"},
		{"empty", "", "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := &Document{Content: tt.content}
			got := formatDocText(doc)
			if got != tt.want {
				t.Errorf("formatDocText(content=%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestFormatDocWithFrontmatter(t *testing.T) {
	t.Parallel()

	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		doc := &Document{
			Name:        "documents/example.com/page",
			URI:         "https://example.com/page",
			Title:       "Example Page",
			Description: "A test page",
			DataSource:  "example.com",
			UpdateTime:  "2026-04-17T00:00:00Z",
			View:        "DOCUMENT_VIEW_CONTENT",
			Content:     "Hello world",
		}
		got, err := formatDocWithFrontmatter(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, "---\n") {
			t.Errorf("expected frontmatter start, got prefix %q", got[:10])
		}
		if !strings.Contains(got, "name: documents/example.com/page") {
			t.Error("expected name in frontmatter")
		}
		if !strings.Contains(got, "uri: https://example.com/page") {
			t.Error("expected uri in frontmatter")
		}
		if !strings.Contains(got, "title: Example Page") {
			t.Error("expected title in frontmatter")
		}
		if !strings.Contains(got, "data_source: example.com") {
			t.Error("expected data source in frontmatter")
		}
		if !strings.HasSuffix(got, "Hello world\n") {
			t.Errorf("expected content at end, got suffix %q", got[len(got)-20:])
		}
	})

	t.Run("empty_content", func(t *testing.T) {
		t.Parallel()
		doc := &Document{
			Name: "documents/example.com/page",
			URI:  "https://example.com/page",
		}
		got, err := formatDocWithFrontmatter(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, "---\n") {
			t.Error("expected frontmatter start")
		}
		if !strings.HasSuffix(got, "\n") {
			t.Error("expected trailing newline")
		}
	})
}

func TestFetchGet(t *testing.T) {
	t.Parallel()

	doc := Document{
		Name:    "documents/example.com/page",
		URI:     "https://example.com/page",
		Content: "Page content",
	}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/documents/example.com/page" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("api key = %q, want %q", got, "test-key")
		}
		json.NewEncoder(w).Encode(doc)
	}))

	url := client.baseURL + "/documents/example.com/page"
	body, err := client.doGet(url)
	if err != nil {
		t.Fatal(err)
	}

	var got Document
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != doc.Name {
		t.Errorf("name = %q, want %q", got.Name, doc.Name)
	}
	if got.Content != doc.Content {
		t.Errorf("content = %q, want %q", got.Content, doc.Content)
	}
}

func TestRunGetPrintsSummaryOnlyForTextOrSizeOnly(t *testing.T) {
	tests := []struct {
		name        string
		format      string
		sizeOnly    bool
		wantSummary bool
	}{
		{name: "text", format: "text", wantSummary: true},
		{name: "json", format: "json", wantSummary: false},
		{name: "jsonl", format: "jsonl", wantSummary: false},
		{name: "yaml", format: "yaml", wantSummary: false},
		{name: "txtar", format: "txtar", wantSummary: false},
		{name: "size_only_json", format: "json", sizeOnly: true, wantSummary: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := Document{
				Name:    "documents/example.com/page",
				URI:     "https://example.com/page",
				Content: "hello\n",
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/documents/example.com/page" {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				json.NewEncoder(w).Encode(doc)
			}))
			t.Cleanup(srv.Close)

			t.Setenv("DEVELOPERKNOWLEDGE_API_KEY", "test-key")
			t.Setenv("GOOGLE_API_KEY", "")

			origBaseURL := searchBaseURL
			origLimiter := apiLimiter
			origOutputFormat := outputFormat
			origOutputFile := outputFile
			origFrontmatter := frontmatter
			origSizeOnly := sizeOnly
			t.Cleanup(func() {
				searchBaseURL = origBaseURL
				apiLimiter = origLimiter
				outputFormat = origOutputFormat
				outputFile = origOutputFile
				frontmatter = origFrontmatter
				sizeOnly = origSizeOnly
			})

			searchBaseURL = srv.URL + "/v1"
			apiLimiter = rate.NewLimiter(rate.Inf, 1)
			outputFormat = tt.format
			outputFile = filepath.Join(t.TempDir(), "out"+formatExtension(tt.format))
			frontmatter = false
			sizeOnly = tt.sizeOnly

			var stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.SetErr(&stderr)
			if err := runGet(cmd, []string{"documents/example.com/page"}); err != nil {
				t.Fatal(err)
			}

			hasSummary := strings.Contains(stderr.String(), "documents/example.com/page")
			if hasSummary != tt.wantSummary {
				t.Fatalf("stderr summary present = %t, want %t; stderr: %q", hasSummary, tt.wantSummary, stderr.String())
			}
		})
	}
}

func TestRunGet_FrontmatterRequiresTextFormat(t *testing.T) {
	tests := []string{"json", "jsonl", "yaml", "txtar"}

	for _, format := range tests {
		t.Run(format, func(t *testing.T) {
			origFrontmatter := frontmatter
			origSizeOnly := sizeOnly
			origOutputFormat := outputFormat
			t.Cleanup(func() {
				frontmatter = origFrontmatter
				sizeOnly = origSizeOnly
				outputFormat = origOutputFormat
			})

			frontmatter = true
			sizeOnly = false
			outputFormat = format

			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())

			err := runGet(cmd, []string{"documents/example.com/page"})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != getFrontmatterTextFormatError {
				t.Fatalf("error = %q, want %q", err.Error(), getFrontmatterTextFormatError)
			}
		})
	}
}

func TestRunGet_FrontmatterRejectsSizeOnly(t *testing.T) {
	origFrontmatter := frontmatter
	origSizeOnly := sizeOnly
	origOutputFormat := outputFormat
	t.Cleanup(func() {
		frontmatter = origFrontmatter
		sizeOnly = origSizeOnly
		outputFormat = origOutputFormat
	})

	frontmatter = true
	sizeOnly = true
	outputFormat = "text"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runGet(cmd, []string{"documents/example.com/page"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != getFrontmatterSizeOnlyError {
		t.Fatalf("error = %q, want %q", err.Error(), getFrontmatterSizeOnlyError)
	}
}
