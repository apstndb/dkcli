package cmd

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var frontmatter bool

var getCmd = &cobra.Command{
	Use:   "get <document-name>",
	Short: "Get a document with its full Markdown content",
	Long: `Retrieve a single document by name.

The document name can be specified as:
  - documents/docs.cloud.google.com/storage/docs/creating-buckets
  - docs.cloud.google.com/storage/docs/creating-buckets
  - https://docs.cloud.google.com/storage/docs/creating-buckets`,
	Args: cobra.ExactArgs(1),
	RunE: runGet,
}

func init() {
	getCmd.Flags().BoolVar(&frontmatter, "frontmatter", false, "prepend YAML frontmatter to content")
	rootCmd.AddCommand(getCmd)
}

// DocumentMeta holds non-content fields for frontmatter.
type DocumentMeta struct {
	Name        string `yaml:"name"`
	URI         string `yaml:"uri"`
	Description string `yaml:"description,omitempty"`
}

func formatDocWithFrontmatter(doc *Document) (string, error) {
	meta := DocumentMeta{
		Name:        doc.Name,
		URI:         doc.URI,
		Description: doc.Description,
	}
	buf, err := yaml.Marshal(meta)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(buf)
	sb.WriteString("---\n")
	sb.WriteString(doc.Content)
	if doc.Content != "" && doc.Content[len(doc.Content)-1] != '\n' {
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func formatDocText(doc *Document) string {
	s := doc.Content
	if s != "" && s[len(s)-1] != '\n' {
		s += "\n"
	}
	return s
}

func runGet(cmd *cobra.Command, args []string) error {
	apiKey, err := getAPIKey()
	if err != nil {
		return err
	}

	name := normalizeDocName(args[0])
	url := baseURL + "/" + name

	body, err := doGet(url, apiKey)
	if err != nil {
		return err
	}

	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}

	if frontmatter {
		s, err := formatDocWithFrontmatter(&doc)
		if err != nil {
			return err
		}
		return printText(s)
	}

	switch outputFormat {
	case "text":
		return printText(formatDocText(&doc))
	case "txtar":
		return printText(txtarEntry(doc.Name, doc.Content))
	default:
		return printFormatted(doc)
	}
}
