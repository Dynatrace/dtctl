package engine

import (
	"context"
	"errors"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/config"
)

// ParseRequest is one command line to analyse. Each field means what it means
// on Request, so a host that restricts Execute gets the same answer from
// Parse. It has no EnvironmentURL or Token: Parse needs neither, and requiring
// them would make a host mint a credential for a call that uses none.
//
// Experimental: Parse and its types may change until a second host uses them.
type ParseRequest struct {
	Command string   // as Request.Command
	Argv    []string // as Request.Argv; takes precedence over Command

	SafetyLevel         string
	Profile             string
	MinStability        string
	StabilityExceptions []string
	NoDeprecated        bool
	// Env is as Request.Env: Profile, when set, overrides an entry for
	// DTCTL_PROFILE.
	Env map[string]string
}

// Parsed is what a command line means, without running it.
//
// Experimental, as Parse.
type Parsed struct {
	Verb     string // "get", "apply", "exec", ...
	Resource string // "" when the verb takes none (apply)

	// InputFiles and OutputFiles are the files the command reads and writes,
	// named the way Request.Files and Result.Files name them (vfs.Key), sorted,
	// de-duplicated and never nil. The stdin values ("-", "@-") are never a path, but
	// ./- is a file named "-" and is reported as one.
	InputFiles  []string
	OutputFiles []string
	// RequiresInput is true when the command may read Request.Stdin: a "-"
	// file argument, "-d @-", a command such as query given no argument and no
	// file, or a command that asks for confirmation (delete, restore) and was
	// given neither its skip flag (--yes, --force) nor --plain or --agent.
	// --dry-run skips the question too, except on delete breakpoint and delete
	// scheduling-rule, which ask before their dry run returns. Without an
	// answer on Stdin that command cancels itself and exits 0, so a host that
	// cannot supply one should pass the skip flag. It is false under
	// --check-scopes, which does not run the command.
	RequiresInput bool

	// MinimumScopes are the exact scopes the command cannot run without: a
	// token that lacks one fails. [] means the command needs no particular
	// scope.
	//
	// MaximumScopes are the scopes the command can use, as patterns: "*"
	// matches any text, including none, and every other character matches
	// itself. A host restricts the command's token to the scopes the user
	// holds that match one. [] means the command has no platform access (it is
	// local, or the command line asks for help); ["*"] means it can use any
	// scope the user holds, which is also the answer for a command whose
	// scopes dtctl does not know.
	//
	// Both are always non-nil, every minimum scope matches a maximum pattern,
	// and both come from fixed lists in dtctl: never from a DQL statement, a
	// payload or an input file. They are only as correct as those lists (the
	// scope catalog, auth.ResourceScopes): a catalog entry that names the
	// wrong scope gives a wrong minimum and a maximum without the scope the
	// command calls, so a token restricted to it fails.
	MinimumScopes []string
	MaximumScopes []string

	// Help is true when the command line asks for help text (--help) or names
	// a group that only prints its usage. Nothing would run: Verb and Resource
	// name the command whose help it is, and a host building policy from them
	// should not read them as a command to run.
	Help bool

	// Flags are the flags the command line set, by long name (-o is
	// "output"), sorted by name. A repeatable flag has one entry per item, in
	// the order given, and a comma-separated slice value is split into its
	// items (--collections a,b is two entries); a flag left at its default is
	// not listed. Args are the positional arguments the command receives, in
	// order. Both are never nil, and empty for Help.
	Flags []ParsedFlag
	Args  []string
}

// ParsedFlag is one flag value a command line set, as the command reads it: a
// boolean is "true" or "false", a count its number.
//
// Experimental, as Parse.
type ParsedFlag struct {
	Name  string // the long name, without dashes
	Value string
}

// UsageError is a command line that Execute would refuse before running the
// command. ExitCode is the exit code Execute returns for it (2 for a usage
// error). Code, Message and Suggestions classify the refusal the way the
// --agent envelope does, and are what Execute prints there for an error in the
// command line itself (an unknown flag, a missing argument). A refusal of the
// surface (profile, stability floor, blocked command) is printed by Execute as
// plain text even under --agent, so for those Code is Parse's classification
// and not text Execute outputs.
type UsageError struct {
	Code        string
	Message     string
	Suggestions []string
	ExitCode    int
}

func (e *UsageError) Error() string { return e.Message }

// Parse analyses a command line the way Execute would run it, and stops before
// the command runs. It reads no file, calls no platform API and needs no token.
//
// Parse and Execute use the same command tree, the same surface masks
// (profile, blocked commands, stability floor, deprecation) and the same
// validation, so a command line Execute would refuse returns a *UsageError
// carrying Execute's error. Validation that runs inside a command body is seen
// by Execute only: a nil error means the command line is well-formed, not that
// Execute will accept it. Any other error is an engine fault.
//
// Safe to call concurrently. It does not count toward any Engine's admission
// budget. It shares the invocation lock with Execute, though: while a
// serialized Execute runs (an Engine built without WithConcurrentExecution, or
// the package-level Execute), Parse waits for it to finish, and ctx bounds the
// wait.
//
// Experimental: Parse and its types may change until a second host uses them.
func Parse(ctx context.Context, req ParseRequest) (*Parsed, error) {
	argv, err := commandArgv(req.Command, req.Argv)
	if err != nil {
		return nil, err
	}
	if err := validateSurface(req.SafetyLevel, req.MinStability, req.StabilityExceptions); err != nil {
		return nil, err
	}
	env := requestEnv(req.Env, req.Profile)
	res, err := cmd.Parse(argv, cmd.ParseOptions{
		SafetyLevel:         config.SafetyLevel(req.SafetyLevel),
		MinStability:        config.StabilityLevel(req.MinStability),
		StabilityExceptions: req.StabilityExceptions,
		NoDeprecated:        req.NoDeprecated,
		Env:                 env,
		BlockedCommands:     unsupportedCommands,
		Context:             ctx,
	})
	var pe *cmd.ParseError
	if errors.As(err, &pe) {
		return nil, &UsageError{Code: pe.Code, Message: pe.Message, Suggestions: pe.Suggestions, ExitCode: pe.ExitCode}
	}
	if err != nil {
		return nil, err
	}
	return &Parsed{
		Verb:          res.Verb,
		Resource:      res.Resource,
		InputFiles:    res.InputFiles,
		OutputFiles:   res.OutputFiles,
		RequiresInput: res.RequiresInput,
		MinimumScopes: res.MinimumScopes,
		MaximumScopes: res.MaximumScopes,
		Help:          res.Help,
		Flags:         parsedFlagsOf(res.Flags),
		Args:          res.Args,
	}, nil
}

func parsedFlagsOf(flags []cmd.ParsedFlag) []ParsedFlag {
	out := make([]ParsedFlag, 0, len(flags))
	for _, f := range flags {
		out = append(out, ParsedFlag{Name: f.Name, Value: f.Value})
	}
	return out
}

// Parse analyses a command line with the rules the package-level Parse
// applies. It takes no execution slot: it does no I/O, and a host must not see
// a parse refused because slow executions hold every slot. On an Engine built
// without WithConcurrentExecution it still waits for a running Execute, as the
// package-level Parse documents.
func (e *Engine) Parse(ctx context.Context, req ParseRequest) (*Parsed, error) {
	return Parse(ctx, req)
}
