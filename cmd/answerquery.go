package cmd

import (
	"fmt"
	"strings"

	dkapi "github.com/apstndb/developerknowledge-go"
	"github.com/spf13/cobra"
)

var answerQueryFilter string

var answerQueryCmd = &cobra.Command{
	Use:   "answer-query <query>...",
	Short: "Answer a query using grounded generation",
	Long: `Answer a query using the Developer Knowledge grounded generation API.

This GA endpoint returns generated text with citations and references to source
document chunks. Text output prints the answer followed by source references;
structured output includes the full answer payload.

--filter sends an AIP-160 filter string verbatim. Filter field names use
snake_case, such as data_source and content_length_bytes, which is independent
of the camelCase JSON names on document objects.

If an API key is configured, dkcli uses it. Otherwise it falls back to ADC.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAnswerQuery,
}

func init() {
	answerQueryCmd.Flags().StringVar(&answerQueryFilter, "filter", "", "AIP-160 filter on document metadata, sent verbatim")
	rootCmd.AddCommand(answerQueryCmd)
}

func (c *apiClient) answerQuery(query, filter string) (*dkapi.AnswerQueryResponse, error) {
	return c.newDKClient().AnswerQuery(c.requestContext(), &dkapi.AnswerQueryRequest{
		Query:  query,
		Filter: filter,
	})
}

func formatAnswerText(answer *dkapi.Answer) string {
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
	client, err := newAPIClientForBaseURL(cmd.Context(), authPreferAPIKey, answerQueryBaseURL)
	if err != nil {
		return err
	}

	resp, err := client.answerQuery(strings.Join(args, " "), answerQueryFilter)
	if err != nil {
		return err
	}

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}

	switch outputFormat {
	case "text":
		_, err = fmt.Fprint(w, formatAnswerText(resp.Answer))
		return finishOutput(err, closer)
	case "txtar":
		_, err = fmt.Fprint(w, txtarEntry("answer.txt", formatAnswerText(resp.Answer)))
		return finishOutput(err, closer)
	default:
		return finishOutput(writeFormatted(w, outputFormat, resp), closer)
	}
}
