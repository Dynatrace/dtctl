package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pipedStdin stands in for `... | dtctl query`: a reader that is not a terminal.
func pipedStdin(content string) queryStdin {
	return queryStdin{r: strings.NewReader(content), isTerminal: false}
}

// terminalStdin stands in for an interactive shell. Reading from it must never
// happen -- that is the hang this package guards against -- so the reader fails
// the test if it is touched.
func terminalStdin(t *testing.T) queryStdin {
	t.Helper()
	return queryStdin{r: failingReader{t: t}, isTerminal: true}
}

type failingReader struct{ t *testing.T }

func (f failingReader) Read([]byte) (int, error) {
	f.t.Error("stdin was read while it is a terminal -- this is the hang")
	return 0, os.ErrClosed
}

func TestResolveQueryInput_InlineArgument(t *testing.T) {
	const want = `fetch logs | filter status == "ERROR"`
	got, err := resolveQueryInput(context.Background(), "", []string{want}, terminalStdin(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveQueryInput_File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.dql")
	if err := os.WriteFile(path, []byte("fetch logs | limit 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveQueryInput(context.Background(), path, nil, terminalStdin(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "fetch logs | limit 1\n" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveQueryInput_FileMissing(t *testing.T) {
	_, err := resolveQueryInput(context.Background(), filepath.Join(t.TempDir(), "nope.dql"), nil, terminalStdin(t))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if !strings.Contains(err.Error(), "failed to read query file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveQueryInput_ExplicitStdin(t *testing.T) {
	got, err := resolveQueryInput(context.Background(), "-", nil, pipedStdin("fetch logs | limit 2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "fetch logs | limit 2" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveQueryInput_PipedStdinWithoutFileFlag(t *testing.T) {
	// `cat query.dql | dtctl query` -- no --file, no argument.
	got, err := resolveQueryInput(context.Background(), "", nil, pipedStdin("fetch logs | limit 3"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "fetch logs | limit 3" {
		t.Fatalf("got %q", got)
	}
}

// `dtctl query -f -` in an interactive shell has nothing to read: io.ReadAll used
// to block until Ctrl+D and then submit an empty query, which looks like a hung
// CLI. It must fail fast with guidance instead.
func TestResolveQueryInput_StdinIsTerminal(t *testing.T) {
	_, err := resolveQueryInput(context.Background(), "-", nil, terminalStdin(t))
	if err == nil {
		t.Fatal("expected an error when --file - is used on a terminal")
	}
	if !strings.Contains(err.Error(), "stdin is a terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "dtctl query -f query.dql") {
		t.Fatalf("error is missing actionable guidance: %v", err)
	}
}

// A PowerShell here-string is a value, not a redirection, so
// `dtctl query -f - @'...'@` puts the query in argv while --file - points at an
// idle terminal. Reporting the conflict beats silently dropping the query.
func TestResolveQueryInput_FileAndInlineQueryConflict(t *testing.T) {
	for _, file := range []string{"-", "query.dql"} {
		t.Run(file, func(t *testing.T) {
			_, err := resolveQueryInput(context.Background(), file, []string{"fetch logs | limit 1"}, terminalStdin(t))
			if err == nil {
				t.Fatal("expected an error when --file and an inline query are combined")
			}
			if !strings.Contains(err.Error(), "both --file and an inline query") {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(err.Error(), "dtctl query -f query.dql") {
				t.Fatalf("error is missing actionable guidance: %v", err)
			}
		})
	}
}

func TestResolveQueryInput_NothingProvided(t *testing.T) {
	_, err := resolveQueryInput(context.Background(), "", nil, terminalStdin(t))
	if err == nil {
		t.Fatal("expected an error when no query source is given")
	}
	if !strings.Contains(err.Error(), "query string or --file is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQueryInputHelp_ShowsOnlyWorkingForms(t *testing.T) {
	help := queryInputHelp()
	if !strings.Contains(help, "dtctl query -f query.dql") {
		t.Errorf("help should mention the file form:\n%s", help)
	}
	// Whatever the platform, the help must never suggest combining `-f -` with
	// an argument -- that is the form that hangs.
	if strings.Contains(help, "-f - @'") {
		t.Errorf("help suggests the hanging form:\n%s", help)
	}
}
