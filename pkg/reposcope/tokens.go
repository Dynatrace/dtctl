package reposcope

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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
	// indirect says, as what the name was read from, why it cannot settle a
	// candidate by itself; "" for a name the unit gives itself. See
	// indirectNames.
	indirect string
	// beside marks a service name read from one of several containers of a
	// workload, and image is that container's image. A manifest lists
	// sidecars beside the unit's own container, so such a name is the unit's
	// only when its container's image is named like the unit.
	beside bool
	image  string
}

// A repository holds names that are not its own, and each of these finds a
// candidate without settling it. The words complete "named only by".
const (
	// The hosting service's name for the repository is often an
	// organisation's or a platform's, shared by everything in it.
	indirectRemote = "the origin remote's name"
	// A manifest lists the images of sidecars and of the products it deploys
	// beside the unit's own.
	indirectImage = "an image a manifest lists"
	// A manifest or chart the unit does not claim by name may deploy a
	// dependency: a cache, a broker, a proxy.
	indirectUnclaimed = "a manifest or chart that is not named like the unit"
	// A workload's second container is a proxy or an agent as a rule, and
	// the service name it is given is that product's.
	indirectSidecar = "a container of a manifest whose image is not named like the unit"
	// What a directory kept for other projects' code, for build output or
	// for test content holds, its own name included.
	indirectForeign = "what is under %s/, which holds no name of this repository's own"
)

// source is where the token was read, "deploy/k8s/deployment.yaml:4".
func (t token) source() string {
	return location(t.file, t.line)
}

// stopWords are names too generic to identify anything: a repository whose
// only signal is "api" or "server" must not be matched against every service
// in an environment.
var stopWords = keySet("app", "api", "server", "service", "svc", "web", "worker", "main",
	"backend", "frontend", "test", "demo", "example", "default", "system", "k8s", "chart")

// layoutWords are what a directory is called for its place in a repository
// rather than for what runs from it: where the sources, the packages, the
// deployment descriptors or one language's code are kept. A directory token
// is held to them besides stopWords, and no other kind is: a workload really
// called core or tools is named so by a manifest or a build file, while
// src/, internal/ or deploy/ is only where a developer happens to stand.
var layoutWords = keySet("internal", "src", "source", "sources", "cmd", "pkg", "lib", "libs", "bin",
	"deploy", "deployment", "deployments", "handlers", "services", "apps", "applications",
	"packages", "modules", "components", "projects", "config", "configs", "scripts", "tools",
	"util", "utils", "common", "shared", "core", "docs", "charts", "helm", "kubernetes",
	"manifests", "infra", "infrastructure", "overlays", "base", "templates", "public", "static",
	"assets", "resources", "java", "python", "dotnet", "golang", "node", "nodejs", "ruby", "php",
	"rust", "kotlin", "scala", "typescript", "javascript")

// testWords name test content. A repository keeps its tests' manifests,
// Dockerfiles and build files beside the real ones, and the names in them
// describe what the tests deploy, not what runs: test-platform-service-a is a
// fixture of platform-service and must not be matched against environments
// as if it were one. The directories and files discovery leaves unread extend
// this set (see excludedDirs and testFileWords); a name is held back when it
// starts with one of these words (see testName).
var testWords = keySet("test", "tests", "mock", "mocks", "fixture", "fixtures")

// minTokenRunes is the shortest name worth sending: two letters match far too
// much to be evidence of anything. maxTokenRunes is the longest: a Kubernetes
// name has at most 253 characters, so a longer string a file holds where a
// name belongs is not one, and would only make the queries longer.
const (
	minTokenRunes = 3
	maxTokenRunes = 253
)

// variants are the spellings a name is matched under: its words (lowercased,
// camelCase split, registry and image tag stripped) joined with "-", "_", "."
// and nothing. Matching on record fields is exact, so "payment_service" in a
// pom.xml has to reach Dynatrace as "payment-service" too. Names in stopWords
// and names shorter than minTokenRunes or longer than maxTokenRunes have none.
// The upper bound is on the longest spelling, the words joined by a
// separator, since every spelling is sent: 252 characters of camelCase are 378
// that way.
func variants(raw string) []string {
	base := baseName(raw)
	words := splitWords(base)
	joined := strings.Join(words, "")
	if len(words) == 0 || stopWords[strings.Join(words, "-")] || stopWords[joined] ||
		utf8.RuneCountInString(joined) < minTokenRunes || longestSpelling(raw) > maxTokenRunes {
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

// longestSpelling is the length in characters of the longest spelling raw is
// sent in: the name as written, or its words joined by a separator.
func longestSpelling(raw string) int {
	base := baseName(raw)
	return max(utf8.RuneCountInString(base), utf8.RuneCountInString(strings.Join(splitWords(base), "-")))
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
// s is read in its composed form, and a combining mark that has none stays
// with its letter: read decomposed, an accented letter would end the word and
// lose its accent.
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(norm.NFC.String(s))
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
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
// placeholder a template, a shell or MSBuild fills in ({{ }}, ${ }, $( )).
func newToken(kind, raw, file string, line int) (token, bool) {
	if strings.Contains(raw, "{{") || strings.Contains(raw, "${") || strings.Contains(raw, "$(") {
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

// testName reports whether the name's first word is one of testWords, as in
// test-platform-service-a or mockLedger. Only the first whole word counts:
// load-test-runner and testkube are names of things that run. A --term is
// never held to this, so a user who means such a name can still send it.
func (t token) testName() bool {
	first, _, _ := strings.Cut(t.value, "-")
	return testWords[first]
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

// union is base with keys added, for a word set that extends another.
func union(base map[string]bool, keys ...string) map[string]bool {
	set := keySet(keys...)
	for k := range base {
		set[k] = true
	}
	return set
}
