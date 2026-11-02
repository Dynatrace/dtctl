package skills

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	dtctlskill "github.com/Dynatrace/dtctl/skills/dtctl"
)

// forbiddenKeychainCommands are OS credential-store CLIs that must never appear
// as an instruction in the shipped skill.
//
// This guard exists because the skill did once carry a
// `security delete-generic-password` recipe for repairing a corrupted keychain
// entry. Agents generalized from it: having been told that `security` is how
// one manages dtctl credentials, a cleanup step would delete the entry and then
// reach for `security dump-keychain` to confirm the token was really gone.
// That verification dumps every keychain in the search list — not just dtctl's
// entries — and with -d prints the secrets, so a teardown step ended up
// spilling the very token it was asked to destroy into the agent transcript.
//
// The skill is instructions, not an enforcement boundary: whatever it shows an
// agent, the agent will extrapolate. So the supported path
// (`dtctl config delete-credentials`, `dtctl auth status` to confirm) must be
// the only credential-store mechanism the skill names.
var forbiddenKeychainCommands = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`\bsecurity\s+(dump-keychain|find-generic-password|delete-generic-password|add-generic-password|find-internet-password)\b`),
		"macOS security(1): dump-keychain and find-*-password print secret material and span every keychain in the search list"},
	{regexp.MustCompile(`\bsecret-tool\s+(lookup|store|clear|search)\b`),
		"Linux secret-tool: lookup/search print secret material"},
	{regexp.MustCompile(`\bcmdkey\s+/(add|delete|list)\b`),
		"Windows cmdkey: /list enumerates stored credentials"},
}

// TestSkillNeverInstructsRawKeychainAccess asserts the shipped skill never tells
// an agent to operate an OS credential store directly. Credentials are reached
// only through dtctl, which sweeps the full set of entries a credential occupies
// and never prints a token.
func TestSkillNeverInstructsRawKeychainAccess(t *testing.T) {
	err := fs.WalkDir(dtctlskill.Content, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		data, readErr := dtctlskill.Content.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for _, region := range splitRegions(string(data)) {
			for _, forbidden := range forbiddenKeychainCommands {
				if !forbidden.pattern.MatchString(region.text) {
					continue
				}
				// Naming a command in order to forbid it is the rule working,
				// not a breach of it — but only in prose. A fenced or indented
				// line is runnable no matter what surrounds it, because an agent
				// lifts it verbatim.
				if region.prose && isProhibition(region.text) {
					continue
				}
				t.Errorf("%s:%d instructs raw keychain access: %s\n  %s\n  use 'dtctl config delete-credentials' to remove a credential and 'dtctl auth status' to confirm it is gone",
					path, region.line, forbidden.reason, strings.TrimSpace(region.text))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded skill content: %v", err)
	}
}

// region is a run of Markdown judged as a unit: a paragraph of prose, or a
// block of code. Prose is judged whole because a prohibition and the command it
// forbids routinely land on different lines of the same wrapped sentence.
type region struct {
	text  string
	line  int  // 1-indexed line where the region starts
	prose bool // false for fenced or indented code, which is always runnable
}

// splitRegions divides Markdown into code blocks and prose paragraphs.
func splitRegions(content string) []region {
	var regions []region
	var buf []string
	bufStart, inFence := 0, false

	flush := func(prose bool) {
		if len(buf) > 0 {
			regions = append(regions, region{text: strings.Join(buf, "\n"), line: bufStart, prose: prose})
			buf = nil
		}
	}

	for i, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			// A fence boundary closes whatever came before it either way.
			flush(!inFence)
			inFence = !inFence
		case inFence, strings.HasPrefix(line, "    "), strings.HasPrefix(line, "\t"):
			if len(buf) == 0 {
				bufStart = i + 1
			}
			buf = append(buf, line)
		case strings.TrimSpace(line) == "":
			flush(true)
		default:
			if len(buf) == 0 {
				bufStart = i + 1
			}
			buf = append(buf, line)
		}
	}
	flush(!inFence)

	return regions
}

// isProhibition reports whether prose names a command in order to warn against
// it rather than to have it run.
//
// Only unambiguous negations count. Softer comparatives ("instead of", "rather
// than", "without") were deliberately left out: "use X without -g" and "check
// it directly rather than dumping" both read as prohibitions to a keyword
// matcher while telling an agent exactly what to run. A hard rule should be
// written as a hard negation, so requiring one costs nothing.
func isProhibition(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"never", "don't", "do not", "must not", "forbidden", "prohibited"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
