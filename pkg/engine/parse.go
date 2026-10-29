package engine

import (
	"context"
	"errors"
	"fmt"

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

	// RequiredScopes are the token scopes the command needs, complete when
	// Scopes is ScopesKnown, and never nil.
	RequiredScopes []string
	Scopes         ScopeRequirement

	// Help is true when the command line asks for help text (--help) or names
	// a group that only prints its usage. Nothing would run: Verb and Resource
	// name the command whose help it is, and a host building policy from them
	// should not read them as a command to run.
	Help bool
}

// ScopeRequirement says how certain Parsed.RequiredScopes is.
type ScopeRequirement string

const (
	// ScopesNone: the catalog names no scopes for the command. For a local
	// command (ctx, commands) that is the answer; for a command the catalog is
	// still missing scopes for, it under-reports.
	ScopesNone ScopeRequirement = "none"
	// ScopesKnown: RequiredScopes is the command's requirement.
	ScopesKnown ScopeRequirement = "known"
	// ScopesPerCall: the scopes are decided by the call (exec api), and
	// resolving them needs the environment's API specification.
	ScopesPerCall ScopeRequirement = "per_call"
	// ScopesInputDependent: the scopes are decided by the input file's
	// content (apply), which Parse does not read.
	ScopesInputDependent ScopeRequirement = "input_dependent"
)

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
	scopes, err := scopeRequirementOf(res.Scopes)
	if err != nil {
		return nil, err
	}
	return &Parsed{
		Verb:           res.Verb,
		Resource:       res.Resource,
		InputFiles:     res.InputFiles,
		OutputFiles:    res.OutputFiles,
		RequiresInput:  res.RequiresInput,
		RequiredScopes: res.RequiredScopes,
		Scopes:         scopes,
		Help:           res.Help,
	}, nil
}

// scopeRequirementOf maps cmd's name for a requirement to its constant. A
// name with no constant is an engine fault, not a value to pass on: a host
// switching on the constants would fall through without noticing.
func scopeRequirementOf(name string) (ScopeRequirement, error) {
	switch r := ScopeRequirement(name); r {
	case ScopesNone, ScopesKnown, ScopesPerCall, ScopesInputDependent:
		return r, nil
	}
	return "", fmt.Errorf("engine: parse: unknown scope requirement %q", name)
}

// Parse analyses a command line with the rules the package-level Parse
// applies. It takes no execution slot: it does no I/O, and a host must not see
// a parse refused because slow executions hold every slot. On an Engine built
// without WithConcurrentExecution it still waits for a running Execute, as the
// package-level Parse documents.
func (e *Engine) Parse(ctx context.Context, req ParseRequest) (*Parsed, error) {
	return Parse(ctx, req)
}
