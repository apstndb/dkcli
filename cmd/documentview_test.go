package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	dkapi "github.com/apstndb/developerknowledge-go"
)

func TestParseDocumentView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    dkapi.DocumentView
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "basic", want: dkapi.DocumentViewBasic},
		{in: "BASIC", want: dkapi.DocumentViewBasic},
		{in: " full ", want: dkapi.DocumentViewFull},
		{in: "content", want: dkapi.DocumentViewContent},
		{in: "DOCUMENT_VIEW_BASIC", want: dkapi.DocumentViewBasic},
		{in: "metadata", wantErr: true},
		{in: "DOCUMENT_VIEW_FUTURE", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, err := parseDocumentView(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDocumentView(%q) error = nil, want error", tt.in)
				}
				if !strings.Contains(err.Error(), "--view must be one of") {
					t.Fatalf("error = %v, want flag guidance", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("parseDocumentView(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDocumentViewOptionsOmitsUnsetView(t *testing.T) {
	t.Parallel()

	if opts := documentViewOptions(""); opts != nil {
		t.Fatalf("documentViewOptions(\"\") = %v, want nil", opts)
	}
	if opts := documentViewOptions(dkapi.DocumentViewBasic); len(opts) != 1 {
		t.Fatalf("documentViewOptions(basic) = %v, want one option", opts)
	}
}

func TestValidateDocumentViewOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		view        dkapi.DocumentView
		format      string
		sizeOnly    bool
		frontmatter bool
		wantErr     bool
	}{
		{name: "basic text rejected", view: dkapi.DocumentViewBasic, format: "text", wantErr: true},
		{name: "basic txtar rejected", view: dkapi.DocumentViewBasic, format: "txtar", wantErr: true},
		{name: "basic text with size-only", view: dkapi.DocumentViewBasic, format: "text", sizeOnly: true},
		{name: "basic text with frontmatter", view: dkapi.DocumentViewBasic, format: "text", frontmatter: true},
		{name: "basic json", view: dkapi.DocumentViewBasic, format: "json"},
		{name: "basic yaml", view: dkapi.DocumentViewBasic, format: "yaml"},
		{name: "basic jsonl", view: dkapi.DocumentViewBasic, format: "jsonl"},
		{name: "content text", view: dkapi.DocumentViewContent, format: "text"},
		{name: "full text", view: dkapi.DocumentViewFull, format: "text"},
		{name: "unset text", view: "", format: "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateDocumentViewOutput(tt.view, tt.format, tt.sizeOnly, tt.frontmatter)
			if tt.wantErr {
				if err == nil {
					t.Fatal("validateDocumentViewOutput() error = nil, want error")
				}
				if !strings.Contains(err.Error(), "returns metadata without content") {
					t.Fatalf("error = %v, want guidance about metadata-only output", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateDocumentViewOutput() error = %v, want nil", err)
			}
		})
	}
}

func TestFetchDocumentSendsDocumentView(t *testing.T) {
	t.Parallel()

	var gotView string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotView = r.URL.Query().Get("view")
		_ = json.NewEncoder(w).Encode(Document{
			Name:               "documents/example.com/a",
			View:               gotView,
			ContentLengthBytes: 31940,
		})
	}))

	opts := documentViewOptions(dkapi.DocumentViewBasic)
	doc, err := client.fetchDocument("documents/example.com/a", opts...)
	if err != nil {
		t.Fatal(err)
	}
	if gotView != string(dkapi.DocumentViewBasic) {
		t.Fatalf("view = %q, want %q", gotView, dkapi.DocumentViewBasic)
	}
	if doc.Content != "" {
		t.Errorf("content = %q, want empty", doc.Content)
	}
	if doc.ContentLengthBytes != 31940 {
		t.Errorf("content length = %d, want 31940", doc.ContentLengthBytes)
	}
}

func TestFetchDocumentOmitsViewByDefault(t *testing.T) {
	t.Parallel()

	sawView := true
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawView = r.URL.Query()["view"]
		_ = json.NewEncoder(w).Encode(Document{Name: "documents/example.com/a", Content: "body"})
	}))

	if _, err := client.fetchDocument("documents/example.com/a"); err != nil {
		t.Fatal(err)
	}
	if sawView {
		t.Fatal("view query parameter sent without --view")
	}
}

func TestFetchBatchDocumentsSendsDocumentView(t *testing.T) {
	t.Parallel()

	var gotView string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotView = r.URL.Query().Get("view")
		docs := []Document{}
		for _, name := range r.URL.Query()["names"] {
			docs = append(docs, Document{Name: name, View: gotView, ContentLengthBytes: 10})
		}
		_ = json.NewEncoder(w).Encode(batchGetResponse{Documents: docs})
	}))

	opts := documentViewOptions(dkapi.DocumentViewBasic)
	docs, docErrs, fatal := client.fetchBatchDocuments(
		[]string{"documents/example.com/a", "documents/example.com/b"},
		opts...,
	)
	if fatal != nil {
		t.Fatal(fatal)
	}
	if len(docErrs) != 0 {
		t.Fatalf("docErrs = %v, want none", docErrs)
	}
	if gotView != string(dkapi.DocumentViewBasic) {
		t.Fatalf("view = %q, want %q", gotView, dkapi.DocumentViewBasic)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d documents, want 2", len(docs))
	}
}

func TestPrintDocSummaryReportsMetadataOnlySize(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDocSummary(&buf, &Document{
		Name:               "documents/example.com/a",
		View:               string(dkapi.DocumentViewBasic),
		ContentLengthBytes: 31940,
	})
	got := buf.String()
	want := "documents/example.com/a (31940 bytes, metadata only)\n"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestPrintDocSummaryReportsZeroByteMetadataOnly(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDocSummary(&buf, &Document{
		Name: "documents/example.com/empty",
		View: string(dkapi.DocumentViewBasic),
	})
	got := buf.String()
	want := "documents/example.com/empty (0 bytes, metadata only)\n"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestPrintDocSummaryPrefersObservedContentOverBasicView(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDocSummary(&buf, &Document{
		Name:               "documents/example.com/unexpected",
		View:               string(dkapi.DocumentViewBasic),
		Content:            "body\n",
		ContentLengthBytes: 5,
	})
	got := buf.String()
	want := "documents/example.com/unexpected (5 bytes, 1 lines)\n"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestPrintDocSummaryKeepsLineCountWithContent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDocSummary(&buf, &Document{
		Name:               "documents/example.com/a",
		Content:            "one\ntwo\n",
		ContentLengthBytes: 8,
	})
	got := buf.String()
	want := "documents/example.com/a (8 bytes, 2 lines)\n"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestPrintDocsSummaryTotalsMetadataOnly(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDocsSummary(&buf, []Document{
		{Name: "documents/example.com/a", ContentLengthBytes: 100},
		{Name: "documents/example.com/b", ContentLengthBytes: 20},
	})
	got := buf.String()
	if !strings.Contains(got, "total: 2 documents, 120 bytes, metadata only\n") {
		t.Fatalf("summary = %q, want metadata-only total of 120 bytes", got)
	}
}
