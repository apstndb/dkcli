package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	frontmatter bool
	sizeOnly    bool
)

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
	getCmd.Flags().BoolVar(&sizeOnly, "size-only", false, "print document size only, suppress content (API calls still occur)")
	rootCmd.AddCommand(getCmd)
}

// DocumentMeta holds non-content fields for frontmatter.
type DocumentMeta struct {
	Name        string `yaml:"name"`
	URI         string `yaml:"uri"`
	Title       string `yaml:"title,omitempty"`
	Description string `yaml:"description,omitempty"`
	DataSource  string `yaml:"data_source,omitempty"`
	UpdateTime  string `yaml:"update_time,omitempty"`
	View        string `yaml:"view,omitempty"`
}

func formatDocWithFrontmatter(doc *Document) (string, error) {
	meta := DocumentMeta{
		Name:        doc.Name,
		URI:         doc.URI,
		Title:       doc.Title,
		Description: doc.Description,
		DataSource:  doc.DataSource,
		UpdateTime:  doc.UpdateTime,
		View:        doc.View,
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
	if !strings.HasSuffix(doc.Content, "\n") {
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func formatDocText(doc *Document) string {
	s := doc.Content
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

func runGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd.Context(), authPreferAPIKey)
	if err != nil {
		return err
	}
	client.baseURL = contentBaseURL

	name := normalizeDocName(args[0])
	body, err := client.fetchDocument(name)
	if err != nil {
		return err
	}

	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}

	printDocSummary(&doc)

	if sizeOnly {
		return nil
	}

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}
	defer closer()

	if frontmatter {
		s, err := formatDocWithFrontmatter(&doc)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(w, s)
		return err
	}

	switch outputFormat {
	case "text":
		_, err := fmt.Fprint(w, formatDocText(&doc))
		return err
	case "txtar":
		_, err := fmt.Fprint(w, txtarEntry(doc.Name, doc.Content))
		return err
	default:
		return writeFormatted(w, outputFormat, doc)
	}
}

func (c *apiClient) fetchDocument(name string) ([]byte, error) {
	url := c.baseURL + "/" + name
	return c.doGet(url)
}
