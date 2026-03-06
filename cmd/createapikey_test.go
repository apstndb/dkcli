package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveProjectID(t *testing.T) {
	// Not parallel: modifies global projectID and env vars.
	orig := projectID
	t.Cleanup(func() { projectID = orig })

	projectID = "from-flag"
	if got := resolveProjectID(); got != "from-flag" {
		t.Errorf("got %q, want %q", got, "from-flag")
	}

	projectID = ""
	t.Setenv("GOOGLE_CLOUD_PROJECT", "from-env")
	if got := resolveProjectID(); got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}

	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "from-sdk")
	if got := resolveProjectID(); got != "from-sdk" {
		t.Errorf("got %q, want %q", got, "from-sdk")
	}

	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	if got := resolveProjectID(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractAPITargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		resp map[string]any
		want []string
	}{
		{
			name: "single_target",
			resp: map[string]any{
				"restrictions": map[string]any{
					"apiTargets": []any{
						map[string]any{"service": "developerknowledge.googleapis.com"},
					},
				},
			},
			want: []string{"developerknowledge.googleapis.com"},
		},
		{
			name: "multiple_targets",
			resp: map[string]any{
				"restrictions": map[string]any{
					"apiTargets": []any{
						map[string]any{"service": "serviceA.googleapis.com"},
						map[string]any{"service": "serviceB.googleapis.com"},
					},
				},
			},
			want: []string{"serviceA.googleapis.com", "serviceB.googleapis.com"},
		},
		{
			name: "no_restrictions",
			resp: map[string]any{},
			want: nil,
		},
		{
			name: "empty_targets",
			resp: map[string]any{
				"restrictions": map[string]any{},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAPITargets(tt.resp)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestQuotaProjectTransport(t *testing.T) {
	t.Parallel()

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-goog-user-project")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{
		Transport: &quotaProjectTransport{
			Base:    http.DefaultTransport,
			Project: "my-project",
		},
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotHeader != "my-project" {
		t.Errorf("x-goog-user-project = %q, want %q", gotHeader, "my-project")
	}
}
