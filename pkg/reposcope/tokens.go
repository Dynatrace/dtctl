package reposcope

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a repository-derived name was read from.
const (
	kindWorkload    = "workload"
	kindNamespace   = "namespace" // evidence only, never sent
	kindImage       = "image"
	kindServiceName = "service-name"
	kindChart       = "chart"
	kindEntrypoint  = "entrypoint"
	kindModule      = "module"
	kindPackage     = "package"
	kindArtifact    = "artifact"
	kindProject     = "project"
	kindAssembly    = "assembly"
	kindRemote      = "remote"
	kindDir         = "dir"
	kindTerm        = "term"
)

// token is one normalised repository-derived name and where it was read: the
// file, and the line within it (0 when unknown).
type token struct {
	kind  string
	value string // variants[0]
	// variants are the name's variants, then its own spelling when that is
	// none of them: record fields compare case-sensitively, so a service
	// that reports itself as BillingEngine is found only under that name.
	variants []string
	file     string
	line     int
}

// source is where the token was read, "deploy/k8s/deployment.yaml:4".
func (t token) source() string {
	return location(t.file, t.line)
}

// stopWords are names too generic to identify anything: a repository whose
// only signal is "api" or "server" must not be matched against every service
// in an environment.
var stopWords = keySet("app", "api", "server", "service", "svc", "web", "worker", "main",
	"backend", "frontend", "test", "demo", "example", "default", "system", "k8s", "chart")

// minTokenRunes is the shortest name worth sending: two letters match far too
// much to be evidence of anything.
const minTokenRunes = 3

// variants are the spellings a name is matched under: its words (lowercased,
// camelCase split, registry and image tag stripped) joined with "-", "_", "."
// and nothing. Matching on record fields is exact, so "payment_service" in a
// pom.xml has to reach Dynatrace as "payment-service" too. Names in stopWords
// and names shorter than minTokenRunes have none.
func variants(raw string) []string {
	words := splitWords(baseName(raw))
	joined := strings.Join(words, "")
	if len(words) == 0 || stopWords[strings.Join(words, "-")] || stopWords[joined] || utf8.RuneCountInString(joined) < minTokenRunes {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, sep := range []string{"-", "_", ".", ""} {
		if v := strings.Join(words, sep); !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// baseName strips what is not part of a name: everything up to the last '/'
// (a registry, a module path, a URL), an image digest and an image tag.
func baseName(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

// splitWords lowercases s and splits it at every non-alphanumeric rune and at
// camelCase boundaries: "paymentService" and "HTTPServer" become
// [payment service] and [http server]. Digits stay with the word before them.
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && i > 0 && startsWord(runes, i) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

// startsWord reports whether the upper-case rune at i begins a word: after a
// lower-case letter or digit ("paymentService"), or as the last capital of an
// acronym followed by a lower-case letter ("HTTPServer").
func startsWord(runes []rune, i int) bool {
	prev := runes[i-1]
	if unicode.IsLower(prev) || unicode.IsDigit(prev) {
		return true
	}
	return unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
}

// newToken builds a token from a raw name read at file:line (line 0 when
// unknown), or reports false when the name is generic, too short, or a
// template placeholder.
func newToken(kind, raw, file string, line int) (token, bool) {
	if strings.Contains(raw, "{{") || strings.Contains(raw, "${") {
		return token{}, false
	}
	v := variants(raw)
	if len(v) == 0 {
		return token{}, false
	}
	if s := strings.TrimSpace(baseName(raw)); PlainName(s) && !slices.Contains(v, s) {
		v = append(v, s)
	}
	return token{kind: kind, value: v[0], variants: v, file: file, line: line}, true
}

// PlainName reports whether s is made only of letters, digits, '-', '_' and
// '.': the characters a name is sent in. A spelling read from the repository
// with anything else is not sent, only its variants are, and a --term with
// anything else is refused.
func PlainName(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.", r) {
			return false
		}
	}
	return s != ""
}

// sameName compares two names in their normalised form, so "payment_service"
// names the directory payment-service.
func sameName(a, b string) bool {
	va, vb := variants(a), variants(b)
	return len(va) > 0 && len(vb) > 0 && va[0] == vb[0]
}

func keySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}
