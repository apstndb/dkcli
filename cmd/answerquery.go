package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var answerQueryCmd = &cobra.Command{
	Use:   "answer-query <query>...",
	Short: "Answer a query using grounded generation",
	Long: `Answer a query using the Developer Knowledge grounded generation API.

This endpoint is currently exposed as v1alpha and returns generated text with
citations and references to source document chunks. Text output prints the
answer followed by source references; structured output includes the full
answer payload.

If an API key is configured, dkcli uses it. Otherwise it falls back to ADC.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAnswerQuery,
}

func init() {
	rootCmd.AddCommand(answerQueryCmd)
}

type answerQueryRequest struct {
	Query string `json:"query"`
}

type Answer struct {
	AnswerText string            `json:"answerText" yaml:"answer_text"`
	Citations  []AnswerCitation  `json:"citations,omitempty" yaml:"citations,omitempty"`
	References []AnswerReference `json:"references,omitempty" yaml:"references,omitempty"`
}

type AnswerCitation struct {
	StartIndex int              `json:"startIndex" yaml:"start_index"`
	EndIndex   int              `json:"endIndex" yaml:"end_index"`
	Sources    []CitationSource `json:"sources,omitempty" yaml:"sources,omitempty"`
}

type CitationSource struct {
	ReferenceIndex int `json:"referenceIndex" yaml:"reference_index"`
}

type AnswerReference struct {
	DocumentReference *DocumentReference `json:"documentReference,omitempty" yaml:"document_reference,omitempty"`
}

type DocumentReference struct {
	DocumentChunk *DocumentChunk `json:"documentChunk,omitempty" yaml:"document_chunk,omitempty"`
}

type answerQueryResponse struct {
	Answer Answer `json:"answer" yaml:"answer"`
}

func (c *apiClient) answerQuery(query string) (*answerQueryResponse, error) {
	reqBody, err := json.Marshal(answerQueryRequest{Query: query})
	if err != nil {
		return nil, err
	}

	body, err := c.doJSONPost(c.baseURL+":answerQuery", reqBody)
	if err != nil {
		return nil, err
	}

	var resp answerQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func formatAnswerText(answer *Answer) string {
	if answer == nil {
		return ""
	}
	if answer.AnswerText == "" && len(answer.References) == 0 {
		return ""
	}

	var sb strings.Builder
	if answer.AnswerText != "" {
		sb.WriteString(answer.AnswerText)
		if !strings.HasSuffix(answer.AnswerText, "\n") {
			sb.WriteByte('\n')
		}
	}

	if len(answer.References) == 0 {
		return sb.String()
	}

	if sb.Len() > 0 {
		sb.WriteByte('\n')
	}
	sb.WriteString("References:\n")
	for i, ref := range answer.References {
		if ref.DocumentReference == nil || ref.DocumentReference.DocumentChunk == nil {
			fmt.Fprintf(&sb, "[%d] unknown reference\n", i+1)
			continue
		}
		chunk := ref.DocumentReference.DocumentChunk
		title := chunk.Parent
		var uri string
		if chunk.Document != nil {
			if chunk.Document.Title != "" {
				title = chunk.Document.Title
			}
			uri = chunk.Document.URI
		}
		if title == "" {
			title = "Untitled"
		}
		if uri != "" {
			fmt.Fprintf(&sb, "[%d] %s - %s\n", i+1, title, uri)
		} else {
			fmt.Fprintf(&sb, "[%d] %s\n", i+1, title)
		}
	}
	return sb.String()
}

func runAnswerQuery(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd.Context(), authPreferAPIKey)
	if err != nil {
		return err
	}
	client.baseURL = answerQueryBaseURL

	resp, err := client.answerQuery(strings.Join(args, " "))
	if err != nil {
		return err
	}

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}
	defer closer()

	switch outputFormat {
	case "text":
		_, err = fmt.Fprint(w, formatAnswerText(&resp.Answer))
		return err
	case "txtar":
		_, err = fmt.Fprint(w, txtarEntry("answer.txt", formatAnswerText(&resp.Answer)))
		return err
	default:
		return writeFormatted(w, outputFormat, resp)
	}
}
