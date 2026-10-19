package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// ParseOptions configures Parse. Each field means what its counterpart on
// Session or RunOptions means for Run, so a caller that restricts what it runs
// gets the same answer from Parse.
type ParseOptions struct {
	SafetyLevel         config.SafetyLevel
	MinStability        config.StabilityLevel
	StabilityExceptions []string
	NoDeprecated        bool

	// Env sets environment variables for the analysis, as RunOptions.Env
	// does for a run (e.g. DTCTL_PROFILE).
	Env map[string]string
	// BlockedCommands removes top-level commands from the surface, as
	// RunOptions.BlockedCommands does.
	BlockedCommands map[string]string
	// Context bounds the wait for a serialized invocation to finish. nil is
	// context.Background().
	Context context.Context
}

// ParseResult is what a command line means, without running it.
type ParseResult struct {
	Verb     string // "get", "apply", "exec", ...
	Resource string // "" when the verb takes none (apply)

	// InputFiles and OutputFiles are the files the command reads and writes,
	// as vfs.Key names, sorted and de-duplicated. "-" is never one.
	InputFiles  []string
	OutputFiles []string
	// RequiresInput is true when the command reads stdin: a "-" file value,
	// "-d @-", or a command such as query given no argument and no file.
	RequiresInput bool

	// RequiredScopes are the token scopes the catalog names for the command,
	// complete only when Scopes is "known". Scopes is "none" (the catalog names
	// none; for a command the catalog is missing scopes for, that
	// under-reports), "known", "per_call" (decided by the call, exec api) or
	// "input_dependent" (decided by the input file's content, apply).
	RequiredScopes []string
	Scopes         string
}

// ParseError is a command line that Execute would refuse before running the
// command body. Code, Message and Suggestions are the error of the --agent
// envelope Execute prints for it, and ExitCode is the exit code it returns.
type ParseError struct {
	Code        string
	Message     string
	Suggestions []string
	ExitCode    int
}

func (e *ParseError) Error() string { return e.Message }

// errParseOnlySession is what a parse-only session's client construction
// returns: Parse must never reach the platform.
var errParseOnlySession = errors.New("a command line being parsed cannot reach the platform")

// Parse analyses a command line the way Run would run it, and stops where the
// command body would start: by then cobra has parsed the flags, validated the
// arguments and required flags, and run the pre-run hooks. It reads no file,
// calls no platform API and needs no token.
//
// It goes through the pipeline Run does — a fresh tree, the same surface
// masks, cobra's validation, the same error classification — so a command
// line Execute would refuse returns a *ParseError with the error Execute
// reports. Validation inside a command body is the one thing it cannot see. Any
// other error is a fault in Parse itself.
//
// Parse runs on a tree of its own and takes the invocation lock shared, like a
// concurrent Run, so it is safe to call from many goroutines.
func Parse(argv []string, opts ParseOptions) (*ParseResult, error) {
	return parseWith(argv, opts, vfs.NewMapFS(nil), strings.NewReader(""))
}

// parseWith is Parse with the filesystem and stdin passed in, so a test can
// watch that neither is touched.
func parseWith(argv []string, opts ParseOptions, fsys vfs.FS, stdin io.Reader) (res *ParseResult, err error) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runMu.RLock(ctx); err != nil {
		return nil, fmt.Errorf("cmd: gave up waiting for a serialized invocation to finish: %w", err)
	}
	defer runMu.RUnlock()
	concurrentActive.Add(1)
	defer concurrentActive.Add(-1)

	// A session, not none: without one, config resolution falls back to the
	// host's config file, profile and floor, which would both read a file and
	// give a different answer than Execute does.
	session := &Session{
		SafetyLevel:         opts.SafetyLevel,
		MinStability:        opts.MinStability,
		StabilityExceptions: opts.StabilityExceptions,
		NoDeprecated:        opts.NoDeprecated,
		parseOnly:           true,
	}
	inv := &invocation{session: session, blocked: opts.BlockedCommands, concurrent: true} // no capabilities
	ctx = withInvocation(ctx, inv)
	defer inv.ended.Store(true)

	cleanup, err := applyRunEnvironment(ctx, RunOptions{Session: session, Env: opts.Env, FS: fsys, Concurrent: true})
	if err != nil {
		return nil, err
	}
	defer cleanup()
	restoreStdio, err := redirectStdio(ctx, io.Discard, io.Discard, stdin)
	if err != nil {
		return nil, err
	}
	defer restoreStdio()
	output.PinColorOff()

	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("cmd: parse: internal error: %v", r)
		}
	}()
	tree := newCommandTree(ctx)
	inv.treeRoot = tree.root
	tree.root.SetOut(currentStdout(ctx))
	tree.root.SetErr(currentStderr(ctx))
	tree.root.SetIn(currentStdin(ctx))
	return parseTree(tree.root, argv)
}

// parseTree is executeTree's pipeline with the command body replaced by a
// record of what cobra resolved. The steps executeTree has and this does not
// have nothing to analyse: tracing, alias resolution (a session has no
// aliases), plugin dispatch (no capability grants it) and the get paging setup.
func parseTree(root *cobra.Command, argv []string) (*ParseResult, error) {
	ctx := cmdContext(root)
	devEnabled, devSignpost := resolveDevelopmentFeatures(ctx, argv)
	setupErrorHandlers(root)

	// Installed where installScopePreflight is, before the surface masks, so a
	// masked command keeps the refusal its mask installs.
	var ran *cobra.Command
	var ranArgs []string
	installParseHook(root, func(c *cobra.Command, args []string) { ran, ranArgs = c, args })

	root.SetArgs(argv)
	if code, err := applySurface(root, argv, devEnabled); err != nil {
		return nil, newParseError(ctx, err, code)
	}
	executed, err := root.ExecuteContextC(ctx)
	if err != nil {
		initConcurrentOnError(ctx)
		err = classifyCommandLineError(root, err, devSignpost)
		return nil, newParseError(ctx, err, exitCodeForError(err))
	}
	if ran == nil {
		// Help, or a group that only prints its usage: no body would run.
		if executed == nil {
			executed = root
		}
		verb, resource := verbResource(executed)
		return &ParseResult{Verb: verb, Resource: resource, Scopes: scopeRequirementNone.String()}, nil
	}
	return newParseResult(ran, ranArgs), nil
}

// installParseHook replaces the body of every runnable command with record.
// A group command's requireSubcommand stays: it is the check that refuses a
// missing or unknown resource (`get`, `get bogus`), and it runs nothing.
func installParseHook(cmd *cobra.Command, record func(*cobra.Command, []string)) {
	for _, sub := range cmd.Commands() {
		installParseHook(sub, record)
	}
	if cmd.RunE != nil && !isRequireSubcommand(cmd.RunE) {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			record(c, args)
			return nil
		}
	}
	if cmd.Run != nil {
		cmd.Run = func(c *cobra.Command, args []string) { record(c, args) }
	}
}

// isRequireSubcommand reports whether run is requireSubcommand itself.
func isRequireSubcommand(run func(*cobra.Command, []string) error) bool {
	return reflect.ValueOf(run).Pointer() == reflect.ValueOf(requireSubcommand).Pointer()
}

func newParseError(ctx context.Context, err error, code int) *ParseError {
	detail := errorToDetail(ctx, err)
	return &ParseError{
		Code:        detail.Code,
		Message:     detail.Message,
		Suggestions: detail.Suggestions,
		ExitCode:    code,
	}
}

// newParseResult reads what the command c would read and write, and its
// scopes, from its parsed flags and the declarations (file_flags.go).
func newParseResult(c *cobra.Command, args []string) *ParseResult {
	verb, resource := verbResource(c)
	res := &ParseResult{Verb: verb, Resource: resource}
	inputs, outputs := map[string]bool{}, map[string]bool{}
	argumentGiven := false

	// c.Flags() holds the inherited persistent flags too once cobra has parsed.
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		// An empty value stands in for nothing: query --dql "" still reads
		// stdin.
		if len(f.Annotations[annotationArgumentFlag]) > 0 && f.Value.String() != "" {
			argumentGiven = true
		}
		kind := firstAnnotation(f, annotationFile)
		if kind == "" {
			return
		}
		for _, v := range flagValues(f) {
			switch {
			case kind == fileValuePath && v == "-", kind == fileValueAtPath && v == "@-":
				res.RequiresInput = true
			case kind == fileValuePath:
				addFileKey(inputs, v)
			case strings.HasPrefix(v, "@"):
				addFileKey(inputs, v[1:])
			}
		}
	})
	c.Flags().VisitAll(func(f *pflag.Flag) {
		target := firstAnnotation(f, annotationWritesTo)
		if target == "" || !f.Changed || f.Value.String() != "true" {
			return
		}
		if t := c.Flags().Lookup(target); t != nil {
			for _, v := range flagValues(t) {
				if v != "-" {
					addFileKey(outputs, v)
				}
			}
		}
	})
	// Only a flag standing in for the argument replaces stdin: query
	// --segments-file names a file, but the query still comes from stdin.
	if c.Annotations[annotationInputWithoutArgs] == "true" && len(args) == 0 && !argumentGiven {
		res.RequiresInput = true
	}
	res.InputFiles, res.OutputFiles = slices.Sorted(maps.Keys(inputs)), slices.Sorted(maps.Keys(outputs))

	required, req := scopesForInvocation(c, verb, resource)
	res.RequiredScopes = append([]string(nil), required...)
	res.Scopes = req.String()
	return res
}

// addFileKey adds the file a value names to set. A value that names no file
// ("", ".", "@" with nothing after it) or names "-" adds nothing: "-" is
// never reported as a path.
func addFileKey(set map[string]bool, value string) {
	if key := vfs.Key(value); key != "" && key != "-" {
		set[key] = true
	}
}

func firstAnnotation(f *pflag.Flag, key string) string {
	if v := f.Annotations[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// flagValues is a flag's value, one entry per item for a slice flag.
func flagValues(f *pflag.Flag) []string {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		return sv.GetSlice()
	}
	return []string{f.Value.String()}
}
