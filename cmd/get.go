package cmd

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	frontmatter bool
	sizeOnly    bool
)

const getFrontmatterTextFormatError = "--frontmatter is only supported with --format=text"
const getFrontmatterSizeOnlyError = "--frontmatter is not supported with --size-only"

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
	getCmd.Flags().BoolVar(&frontmatter, "frontmatter", false, "prepend YAML frontmatter to content (--format=text only; incompatible with --size-only)")
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
	if frontmatter && sizeOnly {
		return errors.New(getFrontmatterSizeOnlyError)
	}
	if frontmatter && outputFormat != "text" {
		return errors.New(getFrontmatterTextFormatError)
	}

	client, err := newAPIClient(cmd.Context(), authPreferAPIKey)
	if err != nil {
		return err
	}

	name := normalizeDocName(args[0])
	body, err := client.fetchDocument(name)
	if err != nil {
		return err
	}

	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}

	if sizeOnly || outputFormat == "text" {
		printDocSummary(cmd.ErrOrStderr(), &doc)
	}

	if sizeOnly {
		return nil
	}

	w, closer, err := outWriter(outputFile)
	if err != nil {
		return err
	}

	if frontmatter {
		s, err := formatDocWithFrontmatter(&doc)
		if err != nil {
			return finishOutput(err, closer)
		}
		_, err = w.Write([]byte(s))
		return finishOutput(err, closer)
	}

	switch outputFormat {
	case "text":
		_, err := w.Write([]byte(formatDocText(&doc)))
		return finishOutput(err, closer)
	case "txtar":
		_, err := w.Write([]byte(txtarEntry(doc.Name, doc.Content)))
		return finishOutput(err, closer)
	default:
		return finishOutput(writeFormatted(w, outputFormat, doc), closer)
	}
}

func (c *apiClient) fetchDocument(name string) ([]byte, error) {
	url := c.baseURL + "/" + name
	return c.doGet(url)
}
