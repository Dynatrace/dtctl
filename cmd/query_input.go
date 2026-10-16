package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// queryStdin is the standard input the query-accepting commands read from. The
// terminal flag is carried alongside the reader so the "nothing to read" case is
// testable without a pty.
type queryStdin struct {
	r          io.Reader
	isTerminal bool
}

// queryWarnOut resolves the stream query-input warnings go to. It must resolve
// currentStderr() at call time, not capture it: the stdio seam swaps that variable
// per invocation (see stdio.go), so a cached writer would send an embedded
// invocation's warning to the host's stderr instead of the request's. It is a
// function variable so tests can capture the output.
var queryWarnOut = currentStderr

// invocationStdin wraps this invocation's stdin: the process's for the CLI
// (swapped by the stdio seam for a serialized embedder), the request's own
// reader for a concurrent one. Only a real file can be a terminal.
func invocationStdin(ctx context.Context) queryStdin {
	in := currentStdin(ctx)
	f, ok := in.(*os.File)
	return queryStdin{r: in, isTerminal: ok && isTerminal(f)}
}

// resolveQueryInput returns the DQL query text for the commands that accept one
// (`query`, `verify query`, `wait`): from --file, from stdin, or from the inline
// positional argument.
//
// It also rejects two combinations that used to fail quietly:
//
//   - --file together with an inline query: the inline query was silently
//     dropped and the file won.
//   - `--file -` while stdin is a terminal: io.ReadAll blocked until the user
//     pressed Ctrl+D and then sent an empty query, which reads as a hung CLI.
//     PowerShell users hit this by transliterating the bash heredoc
//     (`dtctl query -f - <<'EOF'`) into a here-string (`dtctl query -f - @'...'@`).
//     A here-string is a plain string *value*, not a redirection, so nothing
//     ever arrives on stdin.
func resolveQueryInput(ctx context.Context, queryFile string, args []string, stdin queryStdin) (string, error) {
	if queryFile != "" && len(args) > 0 {
		return "", fmt.Errorf("both --file and an inline query were given -- use one or the other\n\n%s", queryInputHelp())
	}

	switch {
	case queryFile == "-":
		if stdin.isTerminal {
			return "", fmt.Errorf("--file - reads the query from stdin, but stdin is a terminal -- nothing to read\n\n%s", queryInputHelp())
		}
		return readAllQuery(stdin.r)
	case queryFile != "":
		// A user-named path goes through the vfs seam: under an embedded
		// invocation the file exists only in the request (see pkg/vfs).
		content, err := vfsEnv(ctx).ReadFile(queryFile)
		if err != nil {
			return "", fmt.Errorf("failed to read query file: %w", err)
		}
		return string(content), nil
	case len(args) > 0:
		// Only an argv-sourced query can have been mangled in transit; a file or
		// a pipe delivers bytes untouched.
		if looksQuoteMangled(rawCommandLine(), args[0]) {
			fmt.Fprintln(queryWarnOut(ctx), quoteMangleWarning(args[0]))
		}
		return args[0], nil
	case !stdin.isTerminal:
		// Piped stdin without --file, e.g. `cat query.dql | dtctl query`.
		return readAllQuery(stdin.r)
	default:
		return "", errors.New("query string or --file is required")
	}
}

func readAllQuery(r io.Reader) (string, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("failed to read query from stdin: %w", err)
	}
	return string(content), nil
}

// queryInputHelp lists the working ways to hand a query to dtctl. On Windows it
// leads with the PowerShell pipe form: passing a query as an argument is where
// Windows PowerShell 5.1 silently strips the double quotes DQL needs for string
// literals, so the pipe is the form that always survives.
func queryInputHelp() string {
	var b strings.Builder
	b.WriteString("Pass the query in one of these ways:\n\n")
	if runtime.GOOS == "windows" {
		b.WriteString("  # PowerShell here-string piped in (quotes always survive)\n")
		b.WriteString("  @'\n")
		b.WriteString("  fetch logs\n")
		b.WriteString("  | filter loglevel == \"INFO\"\n")
		b.WriteString("  '@ | dtctl query\n\n")
		b.WriteString("  # from a file\n")
		b.WriteString("  dtctl query -f query.dql\n\n")
		b.WriteString("  # inline -- but see docs/WINDOWS.md#quoting first:\n")
		b.WriteString("  # Windows PowerShell 5.1 strips the inner double quotes\n")
		b.WriteString("  dtctl query 'fetch logs | limit 10'\n")
		return b.String()
	}
	b.WriteString("  dtctl query 'fetch logs | filter loglevel == \"INFO\"'\n")
	b.WriteString("  dtctl query -f query.dql\n")
	b.WriteString("  cat query.dql | dtctl query\n")
	b.WriteString("  dtctl query -f - <<'EOF'\n  fetch logs\n  EOF\n")
	return b.String()
}
