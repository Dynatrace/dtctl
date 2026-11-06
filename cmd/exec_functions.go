package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

// execFunctionCmd executes an app function or ad-hoc code
var execFunctionCmd = newExecFunctionCmd()

func newExecFunctionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "function [app-id/function-name]",
		Aliases: []string{"fn", "func"},
		Short:   "Execute an app function or ad-hoc JavaScript code",
		Long: `Execute a function from an installed app or run ad-hoc JavaScript code.

App Function Execution:
  Execute a function from an installed app by providing the app ID and function name.

Ad-hoc Code Execution:
  Execute JavaScript code directly without deploying an app.

Examples:
  # Execute an app function (GET)
  dtctl exec function myapp/myfunction

  # Execute with POST and payload
  dtctl exec function myapp/myfunction --method POST --payload '{"key":"value"}'

  # Execute with payload from file (use - to read the payload from stdin)
  dtctl exec function myapp/myfunction --method POST --data payload.json

  # Defer execution (async, for resumable functions)
  dtctl exec function myapp/myfunction --defer

  # Execute ad-hoc JavaScript code
  dtctl exec function --code 'export default async function() { return "hello" }'

  # Execute JavaScript from file
  dtctl exec function -f script.js

  # Execute with payload
  dtctl exec function -f script.js --payload '{"input":"data"}'

  # Pin SDK major versions for ad-hoc code (inline JSON)
  dtctl exec function -f script.js --sdk-major-versions '{"@dynatrace-sdk/adhoc-utils":"1"}'

  # Pin SDK major versions from a file (or @- for stdin)
  dtctl exec function -f script.js --sdk-major-versions @sdk-versions.json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get flags
			method, _ := cmd.Flags().GetString("method")
			payload, _ := cmd.Flags().GetString("payload")
			payloadFile, _ := cmd.Flags().GetString("data")
			sourceCode, _ := cmd.Flags().GetString("code")
			sourceCodeFile, _ := cmd.Flags().GetString("file")
			defer_, _ := cmd.Flags().GetBool("defer")
			sdkVersionsValue, _ := cmd.Flags().GetString("sdk-major-versions")

			// Resolved and validated before setupWithSafety, so a bad value
			// fails before anything touches the network.
			var sdkMajorVersions *map[string]string
			if cmd.Flags().Changed("sdk-major-versions") {
				if sdkVersionsValue == "@-" && (sourceCodeFile == "-" || payloadFile == "-") {
					return fmt.Errorf("--sdk-major-versions @- and %s both read stdin, which can feed only one flag; "+
						"pass one of them as a file", stdinClashFlag(sourceCodeFile))
				}
				versions, err := readSDKMajorVersions(cmdContext(cmd), sdkVersionsValue)
				if err != nil {
					return err
				}
				if (sourceCode == "" && sourceCodeFile == "") || defer_ {
					return exec.ErrSDKMajorVersionsNotAdhoc
				}
				sdkMajorVersions = &versions
			}

			// Ad-hoc JavaScript is unclassifiable by construction: AppEngine injects a
			// bearer on relative fetch(), so the code can POST or DELETE against any
			// platform API the token reaches. It is gated as a delete — the strictest
			// generally-reachable operation — for the same reason `exec api` escalates a
			// request whose operation it cannot resolve. Gating it any lower would make
			// that gate theatre, since a caller blocked on `exec api` could reach the
			// same endpoint through --code. A *named* app function runs the app's own
			// reviewed code and is gated as a create, matching the `exec` verb.
			op := safety.OperationCreate
			if sourceCode != "" || sourceCodeFile != "" {
				op = safety.OperationDelete
			}

			_, c, err := setupWithSafety(cmdContext(cmd), op)
			if err != nil {
				return err
			}

			executor := newFunctionExecutor(cmdContext(cmd), c)

			opts := exec.FunctionExecuteOptions{
				Method:           method,
				Payload:          payload,
				PayloadFile:      payloadFile,
				SourceCode:       sourceCode,
				SourceCodeFile:   sourceCodeFile,
				SDKMajorVersions: sdkMajorVersions,
				Defer:            defer_,
			}

			// Parse function reference from args if provided
			if len(args) > 0 {
				opts.FunctionName = args[0]
			}

			// Execute the function
			result, err := executor.Execute(opts)
			if err != nil {
				return err
			}

			// Handle different result types and print
			printer := newPrinterCtx(cmdContext(cmd))
			return printer.Print(result)
		},
	}
	c.Flags().String("method", "GET", "HTTP method for app function (GET, POST, PUT, PATCH, DELETE)")
	c.Flags().String("payload", "", "request payload (JSON string)")
	c.Flags().String("data", "", "read payload from file (or - for stdin)")
	markFileFlag(c, "data", fileValuePath)
	c.Flags().String("code", "", "JavaScript code to execute (for ad-hoc execution)")
	c.Flags().StringP("file", "f", "", "read JavaScript code from file (for ad-hoc execution)")
	markFileFlag(c, "file", fileValuePath)
	c.Flags().Bool("defer", false, "defer execution (async, for resumable functions)")
	c.Flags().String("sdk-major-versions", "", `pin SDK major versions for ad-hoc execution: a JSON object of package name to version, inline ('{"@dynatrace-sdk/adhoc-utils":"1"}'), @file, or @- for stdin`)
	markFileFlag(c, "sdk-major-versions", fileValueAtPath)
	stability.MarkStable(c)
	stability.MarkFlagStable(c, "sdk-major-versions")
	return c
}

// sdkMajorVersionsExample is the value shown when --sdk-major-versions is
// malformed.
const sdkMajorVersionsExample = `'{"@dynatrace-sdk/adhoc-utils":"1"}'`

// stdinClashFlag names the other flag reading stdin alongside
// --sdk-major-versions @-.
func stdinClashFlag(sourceCodeFile string) string {
	if sourceCodeFile == "-" {
		return "-f -"
	}
	return "--data -"
}

// readSDKMajorVersions resolves the --sdk-major-versions value: inline JSON,
// @file, or @- for stdin. A value that does not start with "@" is never read
// as a path. The result must be a JSON object whose values are all strings;
// package names and versions are left for the API to judge.
func readSDKMajorVersions(ctx context.Context, value string) (map[string]string, error) {
	raw := []byte(value)
	if name, ok := strings.CutPrefix(value, "@"); ok {
		if name == "" {
			return nil, fmt.Errorf("--sdk-major-versions @ needs a filename, or @- to read stdin")
		}
		content, err := readFileFlag(ctx, "sdk-major-versions", name)
		if err != nil {
			return nil, fmt.Errorf("reading --sdk-major-versions from %s: %w", value, err)
		}
		raw = content
	}

	invalid := func(reason string) error {
		return fmt.Errorf("--sdk-major-versions must be a JSON object mapping SDK package names to major versions as strings, "+
			"e.g. %s: %s", sdkMajorVersionsExample, reason)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		// nil without an error is a literal null.
		if err == nil {
			return nil, invalid("got null")
		}
		if json.Valid(raw) {
			return nil, invalid("got a JSON value that is not an object")
		}
		return nil, invalid(fmt.Sprintf("invalid JSON: %v", err))
	}
	versions := make(map[string]string, len(fields))
	for name, v := range fields {
		// json.Unmarshal reads null into a string without complaint, so
		// require the quote explicitly.
		var s string
		if v = bytes.TrimSpace(v); len(v) == 0 || v[0] != '"' || json.Unmarshal(v, &s) != nil {
			return nil, invalid(fmt.Sprintf("the value for %q is %s, not a string", name, string(v)))
		}
		versions[name] = s
	}
	return versions, nil
}

func init() {
	// Function flags
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
