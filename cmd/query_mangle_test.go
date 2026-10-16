package cmd

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestLooksQuoteMangled(t *testing.T) {
	tests := []struct {
		name string
		// raw is the command line as Windows hands it to the process.
		raw string
		// arg is the already-parsed query, i.e. what lands in os.Args.
		arg  string
		want bool
	}{
		{
			// Windows PowerShell 5.1 wraps the argument but leaves the inner
			// quotes bare, so the parser eats them.
			name: "powershell 5.1 legacy passing",
			raw:  `dtctl query "fetch logs | filter loglevel == "INFO" | limit 10"`,
			arg:  `fetch logs | filter loglevel == INFO | limit 10`,
			want: true,
		},
		{
			// PowerShell 7.3+ (Standard/Windows mode) escapes correctly.
			name: "powershell 7.3 standard passing",
			raw:  `dtctl query "fetch logs | filter loglevel == \"INFO\" | limit 10"`,
			arg:  `fetch logs | filter loglevel == "INFO" | limit 10`,
			want: false,
		},
		{
			name: "cmd.exe with escaped quotes",
			raw:  `dtctl query "fetch logs | filter status == \"ERROR\""`,
			arg:  `fetch logs | filter status == "ERROR"`,
			want: false,
		},
		{
			name: "quoted query with no string literals",
			raw:  `dtctl query "fetch logs | limit 10"`,
			arg:  `fetch logs | limit 10`,
			want: false,
		},
		{
			name: "unquoted single-word argument",
			raw:  `dtctl version`,
			arg:  `version`,
			want: false,
		},
		{
			// No spaces, so PowerShell does not wrap it -- the quotes are still
			// consumed, and they sit in the token's interior.
			name: "mangled argument without spaces",
			raw:  `dtctl query loglevel=="INFO"`,
			arg:  `loglevel==INFO`,
			want: true,
		},
		{
			name: "mangled bucket parameter",
			raw:  `dtctl query "fetch logs, bucket:{"custom-logs"} | limit 10"`,
			arg:  `fetch logs, bucket:{custom-logs} | limit 10`,
			want: true,
		},
		{
			// Other quoted flags must not implicate the query argument.
			name: "quoted flag values are not the query",
			raw:  `dtctl query "fetch logs | limit 10" -S "my-segment?host=HOST-001"`,
			arg:  `fetch logs | limit 10`,
			want: false,
		},
		{
			name: "no query on the command line at all",
			raw:  `dtctl query -f query.dql`,
			arg:  "fetch logs | limit 10",
			want: false,
		},
		{
			name: "empty query",
			raw:  `dtctl query ""`,
			arg:  "",
			want: false,
		},
		{
			// POSIX: rawCommandLine() is empty, so detection must stay silent.
			name: "no raw command line available",
			raw:  "",
			arg:  `fetch logs | filter loglevel == INFO`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksQuoteMangled(tt.raw, tt.arg); got != tt.want {
				t.Errorf("looksQuoteMangled(%q, %q) = %v, want %v", tt.raw, tt.arg, got, tt.want)
			}
		})
	}
}

// The decoded tokens must match what the argument parser would produce,
// otherwise the lookup by query text never finds the right token.
func TestParseRawCommandLine_Decoding(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{`dtctl query "fetch logs | limit 10"`, []string{"dtctl", "query", "fetch logs | limit 10"}},
		{`dtctl  query   fetch`, []string{"dtctl", "query", "fetch"}},
		{`dtctl query "a \"b\" c"`, []string{"dtctl", "query", `a "b" c`}},
		{`dtctl query "a "b" c"`, []string{"dtctl", "query", `a b c`}},
		{`dtctl -f C:\path\to\query.dql`, []string{"dtctl", "-f", `C:\path\to\query.dql`}},
		{`dtctl "C:\dir with space\"" x`, []string{"dtctl", `C:\dir with space"`, "x"}},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			tokens := parseRawCommandLine(tt.raw)
			got := make([]string, 0, len(tokens))
			for _, tok := range tokens {
				got = append(got, tok.decoded)
			}
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("parseRawCommandLine(%q) decoded = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestQuoteMangleWarning_Content(t *testing.T) {
	warning := quoteMangleWarning("fetch logs\n| filter loglevel == INFO\n| limit 10")

	// The user cannot see what dtctl received without --verbose, so the warning
	// must show it, collapsed onto one line.
	if !strings.Contains(warning, "dtctl received: fetch logs | filter loglevel == INFO | limit 10") {
		t.Errorf("warning should echo the received query on one line:\n%s", warning)
	}
	for _, want := range []string{"'@ | dtctl query", "dtctl query -f query.dql", "WINDOWS.md#quoting"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning should mention %q:\n%s", want, warning)
		}
	}
}

// End-to-end through the shared resolver: a mangled argv query still runs (we
// cannot know for certain the user meant a string literal), but it warns.
func TestResolveQueryInput_WarnsOnMangledArgument(t *testing.T) {
	var buf strings.Builder
	t.Cleanup(swapQueryWarnOut(&buf))
	t.Cleanup(swapRawCommandLine(`dtctl query "fetch logs | filter loglevel == "INFO" | limit 10"`))

	const arg = `fetch logs | filter loglevel == INFO | limit 10`
	got, err := resolveQueryInput(context.Background(), "", []string{arg}, terminalStdin(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != arg {
		t.Fatalf("query was altered: %q", got)
	}
	if !strings.Contains(buf.String(), "stripped the double quotes") {
		t.Errorf("expected a warning, got: %q", buf.String())
	}
}

func TestResolveQueryInput_NoWarningForWellQuotedArgument(t *testing.T) {
	var buf strings.Builder
	t.Cleanup(swapQueryWarnOut(&buf))
	t.Cleanup(swapRawCommandLine(`dtctl query "fetch logs | filter loglevel == \"INFO\""`))

	if _, err := resolveQueryInput(context.Background(), "", []string{`fetch logs | filter loglevel == "INFO"`}, terminalStdin(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("unexpected warning: %q", buf.String())
	}
}

// A query from a file or a pipe cannot have been mangled, so it must never warn
// even if the command line looks suspicious.
func TestResolveQueryInput_NoWarningForStdinQuery(t *testing.T) {
	var buf strings.Builder
	t.Cleanup(swapQueryWarnOut(&buf))
	t.Cleanup(swapRawCommandLine(`dtctl query -f - "unrelated "quoted" thing"`))

	if _, err := resolveQueryInput(context.Background(), "-", nil, pipedStdin(`fetch logs | filter loglevel == INFO`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("unexpected warning: %q", buf.String())
	}
}

func swapQueryWarnOut(w *strings.Builder) func() {
	prev := queryWarnOut
	queryWarnOut = func(context.Context) io.Writer { return w }
	return func() { queryWarnOut = prev }
}

func swapRawCommandLine(raw string) func() {
	prev := rawCommandLine
	rawCommandLine = func() string { return raw }
	return func() { rawCommandLine = prev }
}
