package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/copilot"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execCopilotCmd executes a Davis CoPilot query
var execCopilotCmd = newExecCopilotCmd()

func newExecCopilotCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "copilot [message]",
		Aliases: []string{"cp", "chat"},
		Short:   "Chat with Davis CoPilot",
		Long: `Send a message to Davis CoPilot and get a response.

Examples:
  # Ask a question
  dtctl exec copilot "What caused the CPU spike on host-123?"

  # Read question from file
  dtctl exec copilot -f question.txt

  # Stream response in real-time
  dtctl exec copilot "Explain the recent errors" --stream

  # Provide additional context
  dtctl exec copilot "Analyze this" --context "Error logs from production"

  # Disable document retrieval (Dynatrace docs)
  dtctl exec copilot "What is DQL?" --no-docs

  # Add formatting instructions
  dtctl exec copilot "List top errors" --instruction "Answer in bullet points"
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := copilot.NewHandler(c)

			// Get message from args or file
			var message string
			inputFile, _ := cmd.Flags().GetString("file")

			switch {
			case inputFile != "":
				content, err := readFileFlag(cmdContext(cmd), "file", inputFile)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}
				message = string(content)
			case len(args) > 0:
				message = args[0]
			default:
				return fmt.Errorf("message is required: provide as argument or use --file")
			}

			// Build options
			stream, _ := cmd.Flags().GetBool("stream")
			contextStr, _ := cmd.Flags().GetString("context")
			instruction, _ := cmd.Flags().GetString("instruction")
			noDocs, _ := cmd.Flags().GetBool("no-docs")

			opts := copilot.ChatOptions{
				Stream:        stream,
				Supplementary: contextStr,
				Instruction:   instruction,
			}

			if noDocs {
				opts.DocumentRetrieval = "disabled"
			}

			// Execute chat
			var result *copilot.ConversationResponse

			if stream {
				_, err = handler.ChatWithOptions(message, opts, func(chunk copilot.StreamChunk) error {
					if chunk.Data != nil && len(chunk.Data.Tokens) > 0 {
						for _, token := range chunk.Data.Tokens {
							fmt.Fprint(currentStdout(cmdContext(cmd)), token)
						}
					}
					return nil
				})
				if err != nil {
					return err
				}
				fmt.Fprintln(currentStdout(cmdContext(cmd))) // Final newline after streaming
			} else {
				result, err = handler.ChatWithOptions(message, opts, nil)
				if err != nil {
					return err
				}
				fmt.Fprintln(currentStdout(cmdContext(cmd)), result.Text)
			}

			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "read message from file, or - for stdin")
	// Each takes its text as the positional argument or from --file: reject
	// only an explicitly empty --file.
	rejectEmptyFlag(c, "file")
	c.Flags().Bool("stream", false, "stream response in real-time")
	c.Flags().String("context", "", "additional context for the conversation")
	stability.MarkFlag(c, "context", stability.Experimental, pre10Since)
	c.Flags().String("instruction", "", "formatting instructions (e.g., 'Answer in bullet points')")
	c.Flags().Bool("no-docs", false, "disable Dynatrace documentation retrieval")
	stability.MarkStable(c)
	return c
}

// execCopilotNl2DqlCmd converts natural language to DQL
var execCopilotNl2DqlCmd = newExecCopilotNl2DqlCmd()

func newExecCopilotNl2DqlCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "nl2dql [text]",
		Short: "Convert natural language to a DQL query",
		Long: `Generate a DQL query from a natural language description.

Examples:
  # Generate DQL from natural language
  dtctl exec copilot nl2dql "show me error logs from the last hour"

  # Read prompt from file
  dtctl exec copilot nl2dql -f prompt.txt

  # Output as JSON (includes messageToken for feedback)
  dtctl exec copilot nl2dql "find hosts with high CPU" -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := copilot.NewHandler(c)

			// Get text from args or file
			var text string
			inputFile, _ := cmd.Flags().GetString("file")

			switch {
			case inputFile != "":
				content, err := readFileFlag(cmdContext(cmd), "file", inputFile)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}
				text = string(content)
			case len(args) > 0:
				text = args[0]
			default:
				return fmt.Errorf("text is required: provide as argument or use --file")
			}

			result, err := handler.Nl2Dql(text)
			if err != nil {
				return err
			}

			// Check output format
			if outputFormat(cmdContext(cmd)) == "" || outputFormat(cmdContext(cmd)) == "table" {
				// Default: just print the DQL
				fmt.Fprintln(currentStdout(cmdContext(cmd)), result.DQL)
				return nil
			}

			printer := newPrinterCtx(cmdContext(cmd))
			return printer.Print(result)
		},
	}
	c.Flags().StringP("file", "f", "", "read prompt from file, or - for stdin")
	rejectEmptyFlag(c, "file")
	stability.MarkStable(c)
	return c
}

// execCopilotDql2NlCmd explains a DQL query in natural language
var execCopilotDql2NlCmd = newExecCopilotDql2NlCmd()

func newExecCopilotDql2NlCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "dql2nl [query]",
		Short: "Explain a DQL query in natural language",
		Long: `Get a natural language explanation of a DQL query.

Examples:
  # Explain a DQL query
  dtctl exec copilot dql2nl "fetch logs | filter status='ERROR' | limit 10"

  # Read query from file
  dtctl exec copilot dql2nl -f query.dql

  # Output as JSON
  dtctl exec copilot dql2nl "fetch logs | limit 10" -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := copilot.NewHandler(c)

			// Get query from args or file
			var query string
			inputFile, _ := cmd.Flags().GetString("file")

			switch {
			case inputFile != "":
				content, err := readFileFlag(cmdContext(cmd), "file", inputFile)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}
				query = string(content)
			case len(args) > 0:
				query = args[0]
			default:
				return fmt.Errorf("query is required: provide as argument or use --file")
			}

			result, err := handler.Dql2Nl(query)
			if err != nil {
				return err
			}

			// Check output format
			if outputFormat(cmdContext(cmd)) == "" || outputFormat(cmdContext(cmd)) == "table" {
				// Default: print summary and explanation
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "Summary: %s\n\n%s\n", result.Summary, result.Explanation)
				return nil
			}

			printer := newPrinterCtx(cmdContext(cmd))
			return printer.Print(result)
		},
	}
	c.Flags().StringP("file", "f", "", "read DQL query from file, or - for stdin")
	rejectEmptyFlag(c, "file")
	stability.MarkStable(c)
	return c
}

// execCopilotDocSearchCmd searches for relevant documents
var execCopilotDocSearchCmd = newExecCopilotDocSearchCmd()

func newExecCopilotDocSearchCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "document-search [query]",
		Aliases: []string{"doc-search", "ds"},
		Short:   "Search for relevant notebooks and dashboards",
		Long: `Search for notebooks and dashboards relevant to your query.

Examples:
  # Search for documents about CPU analysis
  dtctl exec copilot document-search "CPU performance" --collections notebooks

  # Search across multiple collections
  dtctl exec copilot document-search "error monitoring" --collections dashboards,notebooks

  # Exclude specific documents
  dtctl exec copilot document-search "performance" --exclude doc-123,doc-456

  # Output as JSON
  dtctl exec copilot document-search "kubernetes" --collections notebooks -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := copilot.NewHandler(c)

			// Get query from args
			if len(args) == 0 {
				return fmt.Errorf("search query is required")
			}
			query := args[0]

			// Get collections (optional - valid values are undocumented)
			collections, _ := cmd.Flags().GetStringSlice("collections")

			// Get exclude list (optional)
			exclude, _ := cmd.Flags().GetStringSlice("exclude")

			result, err := handler.DocumentSearch([]string{query}, collections, exclude)
			if err != nil {
				return err
			}

			// Check output format
			if outputFormat(cmdContext(cmd)) == "" || outputFormat(cmdContext(cmd)) == "table" {
				printer := newPrinterCtx(cmdContext(cmd))
				return printer.Print(result.Documents)
			}

			printer := newPrinterCtx(cmdContext(cmd))
			return printer.Print(result)
		},
	}
	c.Flags().StringSlice("collections", []string{}, "document collections to search (e.g., notebooks,dashboards)")
	c.Flags().StringSlice("exclude", []string{}, "document IDs to exclude from results")
	stability.MarkStable(c)
	return c
}

func init() {
	// CoPilot subcommands
	execCopilotCmd.AddCommand(execCopilotNl2DqlCmd)
	execCopilotCmd.AddCommand(execCopilotDql2NlCmd)
	execCopilotCmd.AddCommand(execCopilotDocSearchCmd)

	// CoPilot flags
	// Renamed or removed in 1.0 because it hides a global flag
	// (contrib breaking-changes/unshadow-global-flags.md).
	// CoPilot nl2dql flags
	// CoPilot dql2nl flags
	// CoPilot document-search flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
