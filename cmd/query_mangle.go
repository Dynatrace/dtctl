package cmd

import (
	"fmt"
	"strings"
)

// Windows PowerShell 5.1 hands arguments to a native .exe without escaping the
// double quotes inside them. The Windows command-line parser then reads those
// quotes as quoting delimiters and drops them, so
//
//	dtctl query 'fetch logs | filter loglevel == "INFO"'
//
// arrives as `fetch logs | filter loglevel == INFO`. That is *valid* DQL -- a
// comparison between the fields `loglevel` and `INFO` -- so Grail answers with
// zero records and no error, after scanning the whole timeframe. Nothing in the
// response says anything is wrong, which makes it the worst kind of failure to
// debug.
//
// The mangling is detectable, though: it leaves a fingerprint on the raw command
// line that correctly quoted arguments never have. A well-formed argument
// carries delimiter quotes only around its outer edges (anything inside is
// escaped as \"), while a mangled one has bare delimiter quotes in its interior:
//
//	mangled:   "fetch logs | filter loglevel == "INFO" | limit 10"
//	well-formed: "fetch logs | filter loglevel == \"INFO\" | limit 10"
//
// rawCommandLine returns the process's unparsed command line, or "" on platforms
// where the concept does not apply (everything except Windows). It is a variable
// so tests can supply a command line directly.
//
// This is host-process state, so it is meaningless for an embedded invocation:
// under `dtctl serve http` on Windows it is the *server's* command line, not the
// request's. Harmless in practice -- the request's query would have to appear on
// it verbatim as a token carrying interior quotes -- but it is why the detector
// only ever warns and never changes the query.
var rawCommandLine = platformRawCommandLine

// rawToken is one argument of the raw command line, both decoded and described.
type rawToken struct {
	// decoded is the value the argument parser produces, i.e. what shows up in
	// os.Args.
	decoded string
	// interiorQuote records a delimiter quote somewhere other than the token's
	// outer edges -- the fingerprint of quote mangling.
	interiorQuote bool
}

// looksQuoteMangled reports whether arg appears on rawCmdLine as a token whose
// double quotes were consumed by the argument parser. arg is the already-parsed
// value (what dtctl is about to send to Grail).
func looksQuoteMangled(rawCmdLine, arg string) bool {
	if arg == "" || !strings.Contains(rawCmdLine, `"`) {
		return false
	}
	for _, tok := range parseRawCommandLine(rawCmdLine) {
		if tok.decoded == arg && tok.interiorQuote {
			return true
		}
	}
	return false
}

// parseRawCommandLine splits a Windows command line into tokens following the
// Microsoft C runtime rules: whitespace separates tokens outside quotes, a run
// of 2n backslashes before a quote is n backslashes plus a delimiter quote, and
// 2n+1 backslashes before a quote is n backslashes plus a literal quote.
//
// It deliberately omits one rule Go's own argv parsing implements (the "prior to
// 2008" doubled-quote rule, where "" inside a quoted run yields a literal quote:
// see readNextArg in $GOROOT/src/os/exec_windows.go, which is what builds
// os.Args on Windows). A command line using that form therefore decodes
// differently here than it does in os.Args, so the token never matches arg and
// looksQuoteMangled stays silent. That is the safe direction -- doubled quotes
// are a well-formed way to pass a quote through cmd.exe and deserve no warning
// anyway -- and it keeps this splitter simple. Do not rely on the decoded value
// for anything but the equality check against an already-parsed argument.
func parseRawCommandLine(cmdline string) []rawToken {
	var tokens []rawToken

	for i, n := 0, len(cmdline); i < n; {
		for i < n && (cmdline[i] == ' ' || cmdline[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}

		var (
			decoded    strings.Builder
			quoteAt    []int
			inQuotes   bool
			tokenStart = i
		)

		for i < n {
			c := cmdline[i]
			if !inQuotes && (c == ' ' || c == '\t') {
				break
			}

			switch c {
			case '\\':
				slashes := 0
				for i < n && cmdline[i] == '\\' {
					slashes++
					i++
				}
				if i < n && cmdline[i] == '"' {
					decoded.WriteString(strings.Repeat(`\`, slashes/2))
					if slashes%2 == 1 {
						// Escaped: a literal quote, not a delimiter.
						decoded.WriteByte('"')
					} else {
						quoteAt = append(quoteAt, i)
						inQuotes = !inQuotes
					}
					i++
				} else {
					decoded.WriteString(strings.Repeat(`\`, slashes))
				}
			case '"':
				quoteAt = append(quoteAt, i)
				inQuotes = !inQuotes
				i++
			default:
				decoded.WriteByte(c)
				i++
			}
		}

		tokenEnd := i // exclusive
		interior := false
		for _, q := range quoteAt {
			if q != tokenStart && q != tokenEnd-1 {
				interior = true
				break
			}
		}

		tokens = append(tokens, rawToken{decoded: decoded.String(), interiorQuote: interior})
	}

	return tokens
}

// quoteMangleWarning explains what the shell did to the query and how to avoid
// it. It names the query dtctl actually received, because that is the piece a
// user cannot otherwise see without --verbose.
func quoteMangleWarning(query string) string {
	var b strings.Builder
	b.WriteString("Warning: your shell stripped the double quotes from this query.\n")
	fmt.Fprintf(&b, "dtctl received: %s\n", strings.TrimSpace(oneLine(query)))
	b.WriteString("\nDQL string literals need double quotes. Without them this is still valid DQL\n")
	b.WriteString("-- a comparison between two fields -- so it returns no rows instead of an error.\n")
	b.WriteString("\nWindows PowerShell 5.1 does not preserve double quotes in arguments to native\n")
	b.WriteString("programs. Pipe the query in instead, which never goes through argument parsing:\n\n")
	b.WriteString("  @'\n")
	b.WriteString("  fetch logs\n")
	b.WriteString("  | filter loglevel == \"INFO\"\n")
	b.WriteString("  '@ | dtctl query\n\n")
	b.WriteString("Or read it from a file: dtctl query -f query.dql\n")
	b.WriteString("Details: https://github.com/Dynatrace/dtctl/blob/main/docs/WINDOWS.md#quoting\n")
	return b.String()
}

// oneLine collapses a multi-line query so the warning stays compact.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
