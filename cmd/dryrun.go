package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
)

// dryRunPlan is the agent-mode result of a dry run: the mutation the command
// would have performed, and the exact text a human would have been shown.
//
// Message is deliberately part of the payload. A dry run's prose often carries
// detail that no structured field captures ("can take up to a minute", "also
// re-scopes existing breakpoints"), and dropping it in agent mode would make the
// envelope a lossy rendering of the human output rather than a second view of it.
type dryRunPlan struct {
	DryRun   bool              `json:"dry_run"`
	Verb     string            `json:"verb"`
	Resource string            `json:"resource,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
	Payload  json.RawMessage   `json:"payload,omitempty"`
	Message  string            `json:"message"`
}

// dryRunReport collects what a command would have done, so a single call site
// renders both audiences: the plain lines a dry run has always printed, and the
// agent envelope.
//
// Dry-run branches used to print straight to stdout with fmt.Print*. In agent
// mode that put prose on the one stream the caller parses as JSON — while an
// error from the same command arrived correctly enveloped, so the only outcome an
// agent could not read was the successful one. The human rendering is unchanged:
// the lines, their order, and their wording are the contract this type preserves.
type dryRunReport struct {
	cmd     *cobra.Command
	lines   []dryRunLine
	details map[string]string
	payload json.RawMessage
	stderr  bool

	// verb and resource override what the command tree reports; see As.
	verb, resource string
}

// dryRunLine is one line of the human rendering. A line with a label is an
// aligned key-value line (see KV); every other line is printed as text.
type dryRunLine struct {
	text  string
	label string
	width int
}

// plain is the line without terminal styling: what the agent envelope carries
// in its message, and exactly what a human sees with color off.
func (l dryRunLine) plain() string {
	if l.label == "" {
		return l.text
	}
	return l.label + strings.Repeat(" ", kvPadding(l.label, l.width)) + l.text
}

// kvPadding mirrors output.FprintDescribeKV: pad the label to width, and always
// leave at least one space before the value.
func kvPadding(label string, width int) int {
	if pad := width - len(label); pad > 1 {
		return pad
	}
	return 1
}

// newDryRunReport starts a report for a command's dry-run branch. The verb and
// resource in the envelope come from the command itself, so they cannot drift
// from what the caller typed.
func newDryRunReport(cmd *cobra.Command) *dryRunReport {
	return &dryRunReport{cmd: cmd, details: make(map[string]string)}
}

// As sets the verb and resource the envelope reports, for a command nested under
// a group: `account create token` sits below `account`, so the command tree alone
// would report the group as the verb and lose whether the plan creates or
// deletes.
func (r *dryRunReport) As(verb, resource string) *dryRunReport {
	r.verb, r.resource = verb, resource
	return r
}

// OnStderr renders the human lines on stderr instead of stdout. It exists for
// the dry runs that have always written their plan as informational output on
// stderr: moving those lines to stdout would change what a script redirecting
// either stream sees, so the human rendering stays where it was. The agent
// envelope goes to stdout either way, because stdout is the stream an agent
// decodes.
func (r *dryRunReport) OnStderr() *dryRunReport {
	r.stderr = true
	return r
}

// Linef adds a line of prose. It contributes no structured field — use Field for
// anything a caller might want to read back.
func (r *dryRunReport) Linef(format string, args ...interface{}) *dryRunReport {
	r.lines = append(r.lines, dryRunLine{text: fmt.Sprintf(format, args...)})
	return r
}

// Field adds a "Label: value" line and records the value under a snake_case key,
// so the same value is both readable and machine-readable.
func (r *dryRunReport) Field(label, format string, args ...interface{}) *dryRunReport {
	value := fmt.Sprintf(format, args...)
	r.lines = append(r.lines, dryRunLine{text: label + ": " + value})
	r.details[detailKey(label)] = value
	return r
}

// KV adds an aligned key-value line, rendered exactly as output.DescribeKV
// renders it (a bold label padded to width), and records the value under the
// label's snake_case key. It is Field for previews laid out like describe
// output; the envelope's message carries the same line without styling.
func (r *dryRunReport) KV(label string, width int, format string, args ...interface{}) *dryRunReport {
	value := fmt.Sprintf(format, args...)
	r.lines = append(r.lines, dryRunLine{text: value, label: label, width: width})
	r.details[detailKey(label)] = value
	return r
}

// Detail records a structured field without printing a line, for values whose
// human rendering is not "Label: value" — an indented list item, or a line
// padded to align with its neighbours. Field is the common case; this keeps the
// envelope complete for the rest without reformatting what a human sees.
func (r *dryRunReport) Detail(key, format string, args ...interface{}) *dryRunReport {
	r.details[key] = fmt.Sprintf(format, args...)
	return r
}

// Payload records the request body the command would have sent. It is embedded
// as JSON rather than as a string, so an agent can diff a dry run against the
// real request instead of parsing the human preview back out of the message.
// raw must be valid JSON; anything else is dropped rather than risking an
// envelope a caller cannot decode.
func (r *dryRunReport) Payload(raw []byte) *dryRunReport {
	if json.Valid(raw) {
		r.payload = json.RawMessage(raw)
	}
	return r
}

// Print renders the report. It is the return value of a dry-run branch.
func (r *dryRunReport) Print() error {
	ctx := cmdContext(r.cmd)
	if !agentMode(ctx) {
		// Resolved per call, not cached: the streams belong to the invocation.
		w := currentStdout(ctx)
		if r.stderr {
			w = currentStderr(ctx)
		}
		for _, line := range r.lines {
			if line.label != "" {
				output.FprintDescribeKV(w, line.label, line.width, "%s", line.text)
				continue
			}
			fmt.Fprintln(w, line.text)
		}
		return nil
	}

	verb, resource := r.verb, r.resource
	if verb == "" {
		verb, resource = verbResource(r.cmd)
	}
	plan := dryRunPlan{
		DryRun:   true,
		Verb:     verb,
		Resource: resource,
		Message:  r.message(),
	}
	if len(r.details) > 0 {
		plan.Details = r.details
	}
	plan.Payload = r.payload
	// EncodeEnvelope, not a local encoder: it is the one place that decides an
	// envelope's wire form, and it pretty-prints only for a human running --agent
	// at a terminal. A dry run piped to an agent must be compact like every other
	// envelope dtctl emits — indenting it would spend a third more tokens on
	// whitespace, for the one audience this rendering exists to serve.
	return output.EncodeEnvelope(currentStdout(ctx), output.Response{
		OK:      true,
		Result:  plan,
		Context: &output.ResponseContext{Verb: verb, Resource: resource},
	})
}

// message is the human rendering without styling, one line per entry.
func (r *dryRunReport) message() string {
	plain := make([]string, len(r.lines))
	for i, line := range r.lines {
		plain[i] = line.plain()
	}
	return strings.Join(plain, "\n")
}

// detailKey turns a human label into a stable snake_case JSON key ("Display
// Name" -> "display_name"), so renaming a label's capitalisation or spacing does
// not change the payload's shape.
func detailKey(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '.':
			b.WriteByte('_')
		}
	}
	return b.String()
}

// deleteDryRun is the dry-run branch of a delete command: it names the object
// the command would delete. name may be empty when the ID is all there is.
func deleteDryRun(cmd *cobra.Command, kind, name, id string) error {
	report := newDryRunReport(cmd)
	if name == "" || name == id {
		report.Linef("Dry run: would delete %s %q", kind, id)
	} else {
		report.Linef("Dry run: would delete %s %q (%s)", kind, name, id).Detail("name", "%s", name)
	}
	return report.Detail("id", "%s", id).Print()
}

// dryRunCommands are the commands that implement --dry-run: they resolve the
// target, print what they would do and send no mutating request. The flag is
// registered on these commands only, so every other command rejects it
// instead of ignoring it and doing the real work (#477).
//
// Commands that share a RunE must appear together (share/unshare document and
// their dashboard/notebook aliases run the same function, dry-run branch
// included), or the same implementation would accept the flag under one name
// and reject it under another. Guard: TestDryRunFlagFollowsSharedRunE.
//
// query is deliberately absent: it has no dry run, only a warning that the flag
// is meaningless in live mode. `dtctl verify query` is the check-without-running
// command for DQL.
//
// apply and update document define their own --dry-run flag.
var dryRunCommands = []*cobra.Command{
	accountCreateTokenCmd,
	accountCreateServiceUserCmd,
	accountDeleteTokenCmd,
	accountDeleteServiceUserCmd,
	applyExtensionConfigCmd,
	claimEnvironmentShareCmd,
	configDeleteContextCmd,
	configDeleteCredentialsCmd,
	createAnomalyDetectorCmd,
	createAWSConnectionCmd,
	createAWSMonitoringConfigCmd,
	createAzureConnectionCmd,
	createAzureMonitoringConfigCmd,
	createBreakpointCmd,
	createBucketCmd,
	createDashboardCmd,
	createDocumentCmd,
	createEdgeConnectCmd,
	createExtensionCmd,
	createGCPConnectionCmd,
	createGCPMonitoringConfigCmd,
	createLookupCmd,
	createNotebookCmd,
	createSchedulingRuleCmd,
	createSegmentCmd,
	createSettingsCmd,
	createSLOCmd,
	createWorkflowCmd,
	ctxDeleteCmd,
	deleteAnomalyDetectorCmd,
	deleteAppCmd,
	deleteAWSConnectionCmd,
	deleteAWSMonitoringConfigCmd,
	deleteAzureConnectionCmd,
	deleteAzureMonitoringConfigCmd,
	deleteBreakpointCmd,
	deleteBucketCmd,
	deleteDashboardCmd,
	deleteDocumentCmd,
	deleteEdgeConnectCmd,
	deleteGCPConnectionCmd,
	deleteGCPMonitoringConfigCmd,
	deleteLookupCmd,
	deleteNotebookCmd,
	deleteNotificationCmd,
	deleteSchedulingRuleCmd,
	deleteSegmentCmd,
	deleteSettingsCmd,
	deleteSLOCmd,
	deleteTrashCmd,
	deleteWorkflowCmd,
	disableAWSMonitoringCmd,
	disableAzureMonitoringCmd,
	disableGCPMonitoringCmd,
	enableAWSMonitoringCmd,
	enableAzureMonitoringCmd,
	enableGCPMonitoringCmd,
	execAPICmd,
	restoreDashboardCmd,
	restoreDocumentCmd,
	restoreNotebookCmd,
	restoreTrashCmd,
	restoreWorkflowCmd,
	shareDashboardCmd,
	shareDocumentCmd,
	shareNotebookCmd,
	unshareDashboardCmd,
	unshareDocumentCmd,
	unshareNotebookCmd,
	updateAWSConnectionCmd,
	updateAWSMonitoringConfigCmd,
	updateAzureConnectionCmd,
	updateAzureMonitoringConfigCmd,
	updateBreakpointCmd,
	updateExtensionCmd,
	updateExtensionsCmd,
	updateGCPConnectionCmd,
	updateGCPMonitoringConfigCmd,
}

// dryRunUsage is the --dry-run help text, shared by every declaration.
const dryRunUsage = "print what would be done without doing it"

func init() {
	for _, c := range dryRunCommands {
		c.Flags().BoolVar(&gFlags.dryRun, "dry-run", false, dryRunUsage)
	}

	// A hidden root declaration, so that Cobra still knows --dry-run is a
	// boolean when it appears *before* the subcommand. Command lookup strips
	// flags without parsing them, and an undeclared `--dry-run` is assumed to
	// take a value — which swallows the next word: `dtctl --dry-run delete
	// workflow x` resolved to the command "workflow", and `dtctl --dry-run
	// apply -f x.yaml` reported that the root has no dry run. Both spellings
	// worked while the flag was global, so both must keep working.
	//
	// An opted-in command's own --dry-run shadows this one, so it is only ever
	// parsed for a command that has no dry run — which is what
	// rejectUnimplementedDryRun turns into the usage error. Hidden keeps it out
	// of --help and out of the `commands` catalog's global flags.
	rootCmd.PersistentFlags().Bool("dry-run", false, dryRunUsage)
	_ = rootCmd.PersistentFlags().MarkHidden("dry-run")
	rootDryRunFlag = rootCmd.PersistentFlags().Lookup("dry-run")
}

// dryRunPaths is dryRunCommands as root-relative command paths ("delete
// workflow"), so a per-invocation tree can find its own copies of them.
// Computed on first use, after every init() has wired the singleton.
var dryRunPaths = sync.OnceValue(func() map[string]bool {
	paths := make(map[string]bool, len(dryRunCommands))
	for _, c := range dryRunCommands {
		paths[commandPathRelative(c, rootCmd)] = true
	}
	return paths
})

// bindDryRunFlags gives a per-invocation tree's dry-run commands their
// --dry-run, bound to that invocation's flags. The init() above reaches only
// the singleton's commands: without this, a fresh tree's `delete workflow x
// --dry-run` parses the flag into the hidden root declaration, which no
// command reads — and deletes the workflow.
func bindDryRunFlags(root *cobra.Command, flags *rootFlags) {
	paths := dryRunPaths()
	walkCommands(root, func(c *cobra.Command) {
		if paths[commandPathRelative(c, root)] {
			c.Flags().BoolVar(&flags.dryRun, "dry-run", false, dryRunUsage)
		}
	})
}

// rootDryRunFlag is the singleton's hidden root declaration registered above.
// rejectUnimplementedDryRun looks it up through the command's own root, so
// that a per-invocation tree checks its own copy; tests reach it here.
var rootDryRunFlag *pflag.Flag

// dryRunUnavailableMessage is the one wording for "this command has no dry
// run", whether the flag was rejected at parse time or at run time.
func dryRunUnavailableMessage(cmd *cobra.Command) string {
	msg := fmt.Sprintf("unknown flag --dry-run — '%s' has no dry run", cmd.CommandPath())
	if alt := verifyAlternativeFor(cmd); alt != "" {
		msg += fmt.Sprintf("; to check it without running it, use '%s'", alt)
	}
	return msg
}

// verifyAlternativeFor names the `dtctl verify` subcommand that checks what
// this command runs, or "" when none does.
//
// Every verify subcommand is named after the thing it checks, so the typed
// command's own name is the lookup key: `query` and `exec analyzer` resolve to
// `verify query` and `verify analyzer`, and a verify subcommand added later is
// found without touching this function.
//
// The pointer is omitted rather than generalised because a wrong one costs the
// caller a second failed command to discover it was wrong: `verify` has no
// subcommand for a workflow or a login, so "use 'dtctl verify'" dead-ended
// every caller it was written for.
func verifyAlternativeFor(cmd *cobra.Command) string {
	verify, _, err := cmd.Root().Find([]string{"verify"})
	if err != nil || verify.Name() != "verify" {
		return ""
	}
	for _, sub := range verify.Commands() {
		if sub == cmd || !sub.HasAlias(cmd.Name()) && sub.Name() != cmd.Name() {
			continue
		}
		return sub.CommandPath()
	}
	return ""
}

// rejectUnimplementedDryRun fails a command that was given --dry-run but does
// not implement one. The flag reaches the root declaration only when the
// command has none of its own, so its presence there *is* the error.
func rejectUnimplementedDryRun(cmd *cobra.Command) error {
	// The hidden declaration on cmd's own tree: rootDryRunFlag for the
	// singleton, a per-invocation tree's copy for a concurrent embedder.
	rootFlag := cmd.Root().PersistentFlags().Lookup("dry-run")
	if rootFlag == nil || !rootFlag.Changed {
		return nil
	}
	if own := cmd.Flags().Lookup("dry-run"); own != nil && own != rootFlag {
		return nil
	}
	return &suggest.FlagError{Flag: "dry-run", Message: dryRunUnavailableMessage(cmd)}
}
