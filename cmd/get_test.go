package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
			Description: "A test page",
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
		if r.URL.Path != "/v1alpha/documents/example.com/page" {
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
