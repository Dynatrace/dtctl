package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse holds the answers a host relies on: the files a command line
// reads and writes, whether it reads the request's input, and how certain its
// scopes are.
func TestParse(t *testing.T) {
	for name, tc := range map[string]struct {
		command string
		want    Parsed
	}{
		"file flag": {
			command: "create workflow -f wf.yaml",
			want: Parsed{Verb: "create", Resource: "workflow", InputFiles: []string{"wf.yaml"},
				RequiredScopes: []string{"automation:workflows:write"}, Scopes: ScopesKnown},
		},
		"write-back": {
			command: "apply -f ./dashboard.yaml --write-id",
			want: Parsed{Verb: "apply", InputFiles: []string{"dashboard.yaml"}, OutputFiles: []string{"dashboard.yaml"},
				Scopes: ScopesInputDependent},
		},
		"stdin as a file": {
			command: "apply -f -",
			want:    Parsed{Verb: "apply", RequiresInput: true, Scopes: ScopesInputDependent},
		},
		"at-path body": {
			command: "exec api /platform/x -X POST -d @body.json",
			want:    Parsed{Verb: "exec", Resource: "api", InputFiles: []string{"body.json"}, Scopes: ScopesPerCall},
		},
		"inline body": {
			command: `exec api /platform/x -X POST -d '{"a":1}'`,
			want:    Parsed{Verb: "exec", Resource: "api", Scopes: ScopesPerCall},
		},
		"query from stdin": {
			command: "query",
			want:    Parsed{Verb: "query", RequiresInput: true, Scopes: ScopesKnown},
		},
		"output format is not a file": {
			command: "query 'fetch logs' -o json",
			want:    Parsed{Verb: "query", Scopes: ScopesKnown},
		},
		"two files": {
			command: "diff -f a.yaml -f /b.yaml",
			want:    Parsed{Verb: "diff", InputFiles: []string{"a.yaml", "b.yaml"}, Scopes: ScopesInputDependent},
		},
		"empty argument flag still reads stdin": {
			command: `query --dql ""`,
			want:    Parsed{Verb: "query", RequiresInput: true, Scopes: ScopesKnown},
		},
		"no file named": {
			command: "apply -f .",
			want:    Parsed{Verb: "apply", Scopes: ScopesInputDependent},
		},
		"a file named - is not reported": {
			command: "apply -f ./-",
			want:    Parsed{Verb: "apply", Scopes: ScopesInputDependent},
		},
		"empty at-path": {
			command: "exec api /platform/x -X POST -d @",
			want:    Parsed{Verb: "exec", Resource: "api", Scopes: ScopesPerCall},
		},
		"local command": {
			command: "version",
			want:    Parsed{Verb: "version", Scopes: ScopesNone},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(context.Background(), ParseRequest{Command: tc.command})
			require.NoError(t, err)
			if tc.want.Scopes == ScopesKnown && tc.want.RequiredScopes == nil {
				assert.NotEmpty(t, got.RequiredScopes)
				got.RequiredScopes = nil
			}
			assert.Equal(t, &tc.want, got)
		})
	}
}

// TestParse_UsageErrors holds a refused command line to a *UsageError: the
// request's surface (blocked commands, profile, floor) applies as it does to
// Execute, and so does cobra's validation.
func TestParse_UsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		req  ParseRequest
		code string
		exit int
	}{
		"unsupported in a service": {ParseRequest{Command: "config view"}, "unsupported_in_service", 2},
		"outside the profile":      {ParseRequest{Command: "get buckets", Profile: "query"}, "profile_blocked", 2},
		"profile through Env": {
			ParseRequest{Command: "get buckets", Env: map[string]string{"DTCTL_PROFILE": "query"}},
			"profile_blocked", 2,
		},
		"below the floor": {ParseRequest{Command: "get breakpoints"}, "stability_blocked", 2},
		"unknown flag":    {ParseRequest{Command: "get workflows --bogus"}, "unknown_command", 2},
		// Cobra's required-flag error is untyped, so Execute reports it as a
		// generic failure (exit 1) and so does Parse.
		"required flag": {ParseRequest{Command: "apply"}, "error", 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(context.Background(), tc.req)
			var ue *UsageError
			require.ErrorAs(t, err, &ue)
			assert.Equal(t, tc.code, ue.Code)
			assert.Equal(t, tc.exit, ue.ExitCode)
		})
	}

	// An exception admits the below-floor command, as it does for Execute.
	_, err := Parse(context.Background(), ParseRequest{
		Command:             "get breakpoints",
		StabilityExceptions: []string{"get breakpoints"},
	})
	require.NoError(t, err)
}

// TestParse_RequestValidation: a malformed request is a plain error, never a
// *UsageError, so a host can tell its own bug from the caller's command line.
func TestParse_RequestValidation(t *testing.T) {
	for name, req := range map[string]ParseRequest{
		"no command":           {},
		"unparsable quote":     {Command: `create bucket -f "unclosed`},
		"invalid safety level": {Command: "get buckets", SafetyLevel: "anything-goes"},
		"development floor":    {Command: "get buckets", MinStability: "development"},
		"invalid exception":    {Command: "get buckets", StabilityExceptions: []string{"query --decode-snapshots --extra"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(context.Background(), req)
			require.Error(t, err)
			var ue *UsageError
			assert.NotErrorAs(t, err, &ue)
		})
	}
}

// TestEngineParse_TakesNoSlot: a parse is not refused or delayed because
// executions hold every slot.
func TestEngineParse_TakesNoSlot(t *testing.T) {
	e := New(DefaultLimits())
	e.adm.slot <- struct{}{} // the only slot, held
	defer func() { <-e.adm.slot }()

	got, err := e.Parse(context.Background(), ParseRequest{Command: "get workflows"})
	require.NoError(t, err)
	assert.Equal(t, "workflows", got.Resource)
}

func BenchmarkParse(b *testing.B) {
	req := ParseRequest{Command: "apply -f dashboard.yaml --write-id"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}
