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
