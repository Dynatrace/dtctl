package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	resapi "github.com/dynatrace-oss/dtctl/pkg/resources/api"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeAPICmd projects one API's specification.
//
// It never emits the whole document by default. A published specification runs
// from roughly 10k to 47k tokens, which is unreadable for a human and
// unaffordable for an agent, so the default view is the operation index: every
// operation, at a coarser grain. --operation drills into one operation in full,
// and --raw streams the unprojected document for a caller that asks for it.
var describeAPICmd = newDescribeAPICmd()

func newDescribeAPICmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "api <name|base-path>",
		Aliases: []string{"apis"},
		Short:   "Show an API's operations from its published specification",
		Long: `Show what one API offers, from the specification the environment publishes.

The default view is the operation index: every operation as 'METHOD /path' with
its summary and the scope the specification declares for it. That is a complete
view at a coarser grain, not a truncation — drill into any single operation with
--operation to get its parameters, request body, responses, and a ready-to-run
'dtctl exec api' invocation.

An API is named as it appears in 'dtctl get apis', or by its base path.

Note that a blank SCOPE means the specification declares none for that
operation, not that none is required.

Examples:
  # Operation index for one API
  dtctl describe api document

  # One operation in full
  dtctl describe api document --operation 'GET /documents/{id}'

  # Address an API by base path
  dtctl describe api /platform/document/v1

  # The unprojected specification (large — spills to a file in agent mode)
  dtctl describe api document --raw
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := resapi.NewHandler(c)
			entry, err := handler.Resolve(args[0])
			if err != nil {
				return err
			}

			if raw, _ := cmd.Flags().GetBool("raw"); raw {
				doc, err := handler.RawSpec(*entry)
				if err != nil {
					return err
				}
				return emitRawSpec(cmd, cfg, *entry, doc)
			}

			if operation, _ := cmd.Flags().GetString("operation"); operation != "" {
				detail, err := handler.DescribeOperation(*entry, operation)
				if err != nil {
					return err
				}
				if useAPIDescribeTextView(cmdContext(cmd)) {
					printOperationDetail(cmdContext(cmd), detail)
					return nil
				}
				if ap := enrichAgent(printer, "describe", "api"); ap != nil {
					ap.Context().Suggestions = []string{
						detail.Example + "  -- call this operation",
						fmt.Sprintf("dtctl describe api %s  -- the API's other operations", quoteCommandArg(args[0])),
					}
				}
				return printer.Print(detail)
			}

			desc, err := handler.Describe(*entry)
			if err != nil {
				return err
			}
			if useAPIDescribeTextView(cmdContext(cmd)) {
				return printAPIDescribe(cmdContext(cmd), printer, desc)
			}
			if ap := enrichAgent(printer, "describe", "api"); ap != nil {
				ap.Context().Suggestions = apiDescribeSuggestions(args[0], desc)
			}
			return printer.Print(desc)
		},
	}
	c.Flags().String("operation", "", "show one operation in full, addressed as 'METHOD /path'")
	c.Flags().Bool("raw", false, "emit the unprojected specification document")
	stability.MarkStable(c)
	addSpillFlags(c)
	return c
}

// useAPIDescribeTextView reports whether to render the human-readable text view.
// Agent mode always takes the structured path — it leaves outputFormat at its
// "table" default, so a bare format check would emit human text into an agent
// session. "wide" belongs here too: it selects extra columns of the operation
// table, not a different view.
func useAPIDescribeTextView(ctx context.Context) bool {
	if agentMode(ctx) {
		return false
	}
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table" || outputFormat(ctx) == "wide"
}

// apiDescribeSuggestions points at the next step, which for an operation index is
// always drilling into one operation. Naming a real operation from this very
// document beats naming a placeholder.
func apiDescribeSuggestions(query string, d *resapi.APIDescription) []string {
	out := []string{}
	if len(d.Operations) > 0 {
		out = append(out, fmt.Sprintf("dtctl describe api %s --operation '%s'  -- one operation in full",
			quoteCommandArg(query), d.Operations[0].Operation))
	}
	if d.Dtctl != "" {
		out = append(out, fmt.Sprintf("dtctl get %s  -- the native dtctl command for this API (preferred)", d.Dtctl))
	}
	return out
}

// printAPIDescribe renders the human-readable operation index: a header block
// plus the operation list through the ordinary table printer, so the operation
// index looks like every other dtctl listing (and honours -o wide).
func printAPIDescribe(ctx context.Context, printer output.Printer, d *resapi.APIDescription) error {
	const w = 12
	output.FprintDescribeKV(currentStdout(ctx), "Name:", w, "%s", d.Name)
	if d.Title != "" && d.Title != d.Name {
		output.FprintDescribeKV(currentStdout(ctx), "Title:", w, "%s", d.Title)
	}
	if d.Version != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Version:", w, "%s", d.Version)
	}
	if d.Category != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Category:", w, "%s", d.Category)
	}
	output.FprintDescribeKV(currentStdout(ctx), "Base Path:", w, "%s", d.BasePath)
	if d.Summary != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Summary:", w, "%s", d.Summary)
	}
	if d.Dtctl != "" {
		output.FprintDescribeKV(currentStdout(ctx), "dtctl:", w, "%s (prefer the native command over 'exec api')", d.Dtctl)
	}

	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), fmt.Sprintf("Operations (%d):", len(d.Operations)))
	if len(d.Operations) == 0 {
		fmt.Fprintln(currentStdout(ctx), "  (the specification declares none)")
		return nil
	}

	if err := printer.PrintList(d.Operations); err != nil {
		return err
	}

	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintf(currentStdout(ctx), "  Detail:  dtctl describe api %s --operation '%s'\n",
		quoteCommandArg(d.Name), d.Operations[0].Operation)
	return nil
}

// printOperationDetail renders one operation in full.
func printOperationDetail(ctx context.Context, d *resapi.OperationDetail) {
	const w = 12
	output.FprintDescribeKV(currentStdout(ctx), "API:", w, "%s", d.API)
	output.FprintDescribeKV(currentStdout(ctx), "Operation:", w, "%s", d.Operation)
	output.FprintDescribeKV(currentStdout(ctx), "URL:", w, "%s", d.URL)
	if d.OperationID != "" {
		output.FprintDescribeKV(currentStdout(ctx), "ID:", w, "%s", d.OperationID)
	}
	if d.Deprecated {
		output.FprintDescribeKV(currentStdout(ctx), "Deprecated:", w, "%s", "yes")
	}
	if d.Summary != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Summary:", w, "%s", d.Summary)
	}
	if len(d.RequiredScopes) > 0 {
		output.FprintDescribeKV(currentStdout(ctx), "Scopes:", w, "%s", strings.Join(d.RequiredScopes, ", "))
	} else {
		// Saying nothing here would read as "no scope needed", which is not what
		// an absent declaration means.
		output.FprintDescribeKV(currentStdout(ctx), "Scopes:", w, "%s", "(not declared in the specification)")
	}
	if d.Description != "" {
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), indentBlock(unwrapHTMLBreaks(d.Description), "  "))
	}

	printOperationParameters(ctx, d.Parameters)
	printOperationBody(ctx, d.RequestBody)
	printOperationResponses(ctx, d.Responses)

	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintf(currentStdout(ctx), "  Call it:  %s\n", d.Example)
}

func printOperationParameters(ctx context.Context, params []resapi.Parameter) {
	if len(params) == 0 {
		return
	}
	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), "Parameters:")

	nameW := 0
	for _, p := range params {
		if len(p.Name) > nameW {
			nameW = len(p.Name)
		}
	}
	for _, p := range params {
		req := ""
		if p.Required {
			req = " (required)"
		}
		typ := p.Type
		if typ == "" {
			typ = "-"
		}
		fmt.Fprintf(currentStdout(ctx), "  %-*s  %-6s  %-8s  %s%s\n", nameW, p.Name, p.In, typ,
			firstLineOf(p.Description), req)
	}
}

func printOperationBody(ctx context.Context, body *resapi.RequestBody) {
	if body == nil || len(body.Contents) == 0 {
		return
	}
	fmt.Fprintln(currentStdout(ctx))
	title := "Request Body:"
	if body.Required {
		title = "Request Body (required):"
	}
	output.FprintDescribeSection(currentStdout(ctx), title)

	for _, mt := range body.Contents {
		fmt.Fprintf(currentStdout(ctx), "  %s\n", mt.ContentType)
		fields, ok := resapi.TopLevelFields(mt.Schema)
		if !ok {
			fmt.Fprintln(currentStdout(ctx), "    (schema not introspectable — use -o yaml for the full schema)")
			continue
		}
		printSchemaFieldLines(ctx, fields)
	}
}

func printOperationResponses(ctx context.Context, responses []resapi.Response) {
	if len(responses) == 0 {
		return
	}
	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), "Responses:")
	for _, r := range responses {
		types := make([]string, 0, len(r.Contents))
		for _, mt := range r.Contents {
			types = append(types, mt.ContentType)
		}
		line := fmt.Sprintf("  %-5s %s", r.Status, firstLineOf(r.Description))
		if len(types) > 0 {
			line += fmt.Sprintf("  [%s]", strings.Join(types, ", "))
		}
		fmt.Fprintln(currentStdout(ctx), line)
	}
}

func printSchemaFieldLines(ctx context.Context, fields []resapi.SchemaField) {
	if len(fields) == 0 {
		fmt.Fprintln(currentStdout(ctx), "    (no properties declared)")
		return
	}
	nameW := 0
	for _, f := range fields {
		if len(f.Name) > nameW {
			nameW = len(f.Name)
		}
	}
	for _, f := range fields {
		req := ""
		if f.Required {
			req = " (required)"
		}
		typ := f.Type
		if typ == "" {
			typ = "-"
		}
		fmt.Fprintf(currentStdout(ctx), "    %-*s  %-12s  %s%s\n", nameW, f.Name, typ, f.Description, req)
	}
	fmt.Fprintln(currentStdout(ctx), "    (one level shown — use -o yaml for the full schema)")
}

// emitRawSpec writes the unprojected specification.
//
// A raw document is far too large to hand to an agent inline, so it goes through
// the same result-spill controls as a large query result: outside agent mode the
// default is to stream it to stdout (where a human redirects it), and in agent
// mode it spills to a file above the spill threshold. Spilling is gated on the
// HostDiskSpill capability, so an embedded caller — which has no host disk of its
// own — always gets the bytes back instead of a path it could not read.
//
// A document that is not spilled goes to stdout unwrapped even in agent mode,
// for the same reason the query path leaves a non-JSON display encoding alone:
// --raw is an explicit request for the document as served, and wrapping it would
// discard the format the caller asked for.
func emitRawSpec(cmd *cobra.Command, cfg *config.Config, entry resapi.Entry, doc []byte) error {
	opts, err := resolveSpillOptions(cmd, cfg)
	if err != nil {
		return err
	}

	size := int64(len(doc))
	spill := opts.Mode == exec.SpillAlways ||
		(opts.Mode == exec.SpillAuto && size > opts.Threshold)
	if !spill {
		if _, werr := currentStdout(cmdContext(cmd)).Write(doc); werr != nil {
			return werr
		}
		// Only when a human is watching, and never under --agent: --raw is documented
		// as going to stdout so the caller can redirect it, and a redirected document
		// must be byte-exact.
		if writerIsTerminal(currentStdout(cmdContext(cmd))) && !agentMode(cmdContext(cmd)) && size > 0 && doc[size-1] != '\n' {
			fmt.Fprintln(currentStdout(cmdContext(cmd)))
		}
		return nil
	}

	// A raw document is written in the encoding the environment served it in, so
	// --spill-format has nothing to choose.
	if cmd.Flags().Changed("spill-format") {
		output.FprintWarning(currentStderr(cmdContext(cmd)), "--spill-format does not apply to --raw: a specification is written in its own encoding")
	}

	encoding := resapi.RawSpecEncoding(entry)
	targetPath, baseDir, err := rawSpecTargetPath(cmdContext(cmd), cfg, entry, encoding, opts)
	if err != nil {
		return err
	}

	written, err := output.WriteSpillFile(targetPath, func(w io.Writer) error {
		_, werr := w.Write(doc)
		return werr
	})
	if err != nil {
		return fmt.Errorf("failed to write %q: %w", targetPath, err)
	}
	// Opportunistic, throttled TTL prune of the managed cache — a spilled document
	// is subject to the same exposure window as a spilled result.
	if baseDir != "" {
		output.PruneOldSpills(baseDir, opts.TTL)
	}

	if agentMode(cmdContext(cmd)) {
		printer := newPrinterCtx(cmdContext(cmd))
		if ap := enrichAgent(printer, "describe", "api"); ap != nil {
			ap.Context().Suggestions = []string{
				"the file holds the specification exactly as served; read it locally",
				fmt.Sprintf("dtctl describe api %s  -- the projected operation index (far smaller)",
					quoteCommandArg(entry.Name)),
			}
		}
		return printer.Print(&output.SpecFileManifest{
			Kind:     output.KindDocumentFile,
			Path:     targetPath,
			Format:   encoding,
			Bytes:    written,
			API:      entry.Name,
			Document: entry.URL,
		})
	}

	fmt.Fprintf(currentStdout(cmdContext(cmd)), "Wrote %s (%d bytes) to %s\n", entry.Name, written, targetPath)
	return nil
}

// rawSpecTargetPath resolves where a raw specification is written: an explicit
// --spill-to destination, or the managed spill cache partitioned by context. The
// returned baseDir is non-empty only for the managed cache, which is the only
// location dtctl prunes on a TTL.
func rawSpecTargetPath(ctx context.Context, cfg *config.Config, entry resapi.Entry, encoding string, opts exec.SpillOptions) (path, baseDir string, err error) {
	if opts.ToPath != "" {
		dir := filepath.Dir(opts.ToPath)
		if !output.ProbeWritable(dir) {
			return "", "", fmt.Errorf("spill destination directory %q is not writable", dir)
		}
		output.FprintWarning(currentStderr(ctx), "%s", userChosenSpillPathWarning)
		return opts.ToPath, "", nil
	}

	base, managed, err := output.SpillBaseDir(opts.Dir)
	if err != nil {
		return "", "", fmt.Errorf("--raw needs a writable location to spill to: %w", err)
	}
	dir := base
	_, contextName := spillProvenance(cfg)
	if managed {
		dir = filepath.Join(base, output.SanitizeContextName(contextName))
	} else {
		base = ""
		output.FprintWarning(currentStderr(ctx), "%s", userChosenSpillPathWarning)
	}

	// The filename is derived from the document's request path, and the hash is
	// computed locally: nothing the environment served is ever used verbatim as a
	// filename.
	name := "spec-" + output.SpillHash(contextName, entry.URL) + "." + encoding
	return filepath.Join(dir, name), base, nil
}

// userChosenSpillPathWarning mirrors the query path's warning: a caller-chosen
// destination opts out of the managed guarantees.
const userChosenSpillPathWarning = "spill path is a user-chosen location and opts out of the managed privacy guarantees (no TTL pruning, no per-context partitioning, best-effort 0600 only); you own its lifetime"

// quoteCommandArg quotes a value for a copy-pasteable command line. API names
// routinely contain spaces, so a suggestion that omits the quotes is a suggestion
// that does not run.
func quoteCommandArg(s string) string { return resapi.QuoteArg(s) }

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// unwrapHTMLBreaks turns the <br/> tags that OpenAPI descriptions use for
// rendered documentation into real line breaks. Left alone they run paragraphs
// together in a terminal.
func unwrapHTMLBreaks(s string) string {
	for _, tag := range []string{"<br/>", "<br />", "<br>"} {
		s = strings.ReplaceAll(s, tag, "\n")
	}
	return strings.TrimSpace(s)
}

func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			// No trailing indent on a blank line.
			continue
		}
		lines[i] = indent + l
	}
	return strings.Join(lines, "\n")
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
