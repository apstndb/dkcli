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

This endpoint is currently exposed as v1alpha.

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
	AnswerText string `json:"answerText" yaml:"answer_text"`
}

type answerQueryResponse struct {
	Answer Answer `json:"answer" yaml:"answer"`
}

func (c *apiClient) answerQuery(query string) (*answerQueryResponse, error) {
	reqBody, err := json.Marshal(answerQueryRequest{Query: query})
	if err != nil {
		return nil, err
	}

	body, err := c.doJSONPost(answerQueryURL, reqBody)
	if err != nil {
		return nil, err
	}

	var resp answerQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func runAnswerQuery(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd.Context(), authPreferAPIKey)
	if err != nil {
		return err
	}

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
		_, err = fmt.Fprintln(w, resp.Answer.AnswerText)
		return err
	case "txtar":
		_, err = fmt.Fprint(w, txtarEntry("answer.txt", resp.Answer.AnswerText))
		return err
	default:
		return writeFormatted(w, outputFormat, resp)
	}
}
