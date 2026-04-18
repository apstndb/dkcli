package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
			Answer: Answer{AnswerText: "dkcli is a CLI for Developer Knowledge."},
		})
	}))
	t.Cleanup(srv.Close)

	origURL := answerQueryURL
	answerQueryURL = srv.URL + "/v1alpha:answerQuery"
	t.Cleanup(func() { answerQueryURL = origURL })

	client := &apiClient{
		baseURL: srv.URL + "/v1",
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
}
