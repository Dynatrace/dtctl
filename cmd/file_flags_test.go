package cmd

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// fileFlagProbeHints are flags a command needs before it reaches its file
// read. Without them it fails earlier and the guard below sees nothing.
var fileFlagProbeHints = map[string][]string{
	"exec api":           {"--method", "POST"}, // -d needs an explicit method
	"inventory arrivals": {"--scope", `service.name == "probe"`},
	"wait query":         {"--for", "any"},
}

// fileFlagUnreached lists declared file flags and read call sites the probe
// does not reach, each with the reason. The list can only shrink: an entry
// the probe reaches fails the test until it is removed.
var fileFlagUnreached = map[string]string{
	"apply --write-id": "it writes only after a successful apply, which needs a server",
}

// TestFileFlagsAreDeclared holds that every flag naming a file a command reads
// is declared with markFileFlag, so a caller can learn what a command line
// reads without running it. It is keyed on the filesystem, not on helper
// names: every command runs with each of its string flags set to a sentinel
// path, as a plain value and as "@path", on a recording filesystem, and every
// sentinel read must come from a flag declared with that kind.
//
// Two coverage checks make "an undeclared file flag fails a test" hold
// rather than depend on what the probe happens to reach:
//   - every declared flag is seen reading, so a declaration cannot go stale;
//   - every call to readFileFlag, readFileFlagFrom, resolveQueryInput and
//     readRequestBody in cmd/ is on the stack of a sentinel read, so a new
//     read the probe cannot reach fails until it is reachable or listed in
//     fileFlagUnreached.
//
// Reads that skip those helpers and take the filesystem directly are pinned
// by TestVFSEnvCallSitesAreKnown.
func TestFileFlagsAreDeclared(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 400, not 5xx: the client retries server errors, and the probe only
		// needs every call to fail fast.
		http.Error(w, `{"error":{"code":400,"message":"file flag probe"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	calls := fileReadCallSites(t) // before isolateProbeHost leaves the package directory
	isolateProbeHost(t)

	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	probes := fileFlagProbes(root)
	require.NotEmpty(t, probes)

	reads := make([][]recordedRead, len(probes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, p := range probes {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			reads[i] = runFileFlagProbe(srv.URL, p)
		}()
	}
	wg.Wait()

	declared := declaredFileFlags(root)
	seen := map[string]bool{}  // "<command> --<flag>" seen reading with its declared kind
	sites := map[string]bool{} // "file.go:line" on the stack of a sentinel read
	var undeclared []string
	for i, p := range probes {
		for _, r := range reads[i] {
			key, kind, ok := p.attribute(r.name)
			if !ok {
				continue
			}
			for _, s := range r.stack {
				sites[s] = true
			}
			switch got := declared[key]; {
			case got == "":
				undeclared = append(undeclared, fmt.Sprintf(
					"%s reads a file (%s) but is not declared; add markFileFlag(c, %q, fileValue%s) to its constructor",
					key, kind, flagOf(key), kindConst(kind)))
				continue
			case got != kind:
				undeclared = append(undeclared, fmt.Sprintf("%s is declared %s but reads a file as %s; declare it with fileValue%s",
					key, got, kind, kindConst(kind)))
				continue
			}
			seen[key] = true
		}
	}
	for _, u := range dedupe(undeclared) {
		t.Error(u)
	}

	for key := range declared {
		_, listed := fileFlagUnreached[key]
		switch {
		case seen[key] && listed:
			t.Errorf("%s is listed in fileFlagUnreached, but the probe reaches it: remove the entry", key)
		case !seen[key] && !listed:
			t.Errorf("%s is declared as a file flag but never read a sentinel path: "+
				"fix the declaration, add a fileFlagProbeHints entry, or list it in fileFlagUnreached", key)
		}
	}

	for _, call := range calls {
		_, listed := fileFlagUnreached[call.key()]
		reached := call.reachedBy(sites)
		switch {
		case reached && listed:
			t.Errorf("%s is listed in fileFlagUnreached, but the probe reaches it: remove the entry", call.key())
		case !reached && !listed:
			t.Errorf("%s:%d: the %s call in %s reads a file the probe never reaches, so an undeclared flag there "+
				"would go unnoticed; add a fileFlagProbeHints entry or list %q in fileFlagUnreached",
				call.file, call.start, call.fn, call.caller, call.key())
		}
	}
}

// stabilityAnnotationKeys are pkg/stability's keys, the other dtctl.io/
// annotations a command or flag may carry.
var stabilityAnnotationKeys = []string{
	stability.AnnotationLevel,
	stability.AnnotationSince,
	stability.AnnotationFeature,
	stability.AnnotationDeprecatedSince,
	stability.AnnotationDeprecatedRemoveIn,
	stability.AnnotationDeprecatedReplacement,
}

// TestFileFlagAnnotationsAreValid holds the declarations' values: a typo in a
// key or value would silently drop a file from a caller's view.
func TestFileFlagAnnotationsAreValid(t *testing.T) {
	known := map[string]bool{
		annotationFile:             true,
		annotationWritesTo:         true,
		annotationInputWithoutArgs: true,
		annotationScopes:           true,
		annotationArgumentFlag:     true,
	}
	for _, k := range stabilityAnnotationKeys {
		known[k] = true
	}
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	walkCommands(root, func(c *cobra.Command) {
		for k, v := range c.Annotations {
			if strings.HasPrefix(k, "dtctl.io/") && !known[k] {
				t.Errorf("%s: unknown annotation %q", c.CommandPath(), k)
			}
			if k == annotationInputWithoutArgs && v != "true" {
				t.Errorf("%s: %s = %q, want \"true\"", c.CommandPath(), k, v)
			}
			if k == annotationScopes && v != scopesInputDependent {
				t.Errorf("%s: %s = %q, want %q", c.CommandPath(), k, v, scopesInputDependent)
			}
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			for k, v := range f.Annotations {
				if strings.HasPrefix(k, "dtctl.io/") && !known[k] {
					t.Errorf("%s --%s: unknown annotation %q", c.CommandPath(), f.Name, k)
				}
				switch k {
				case annotationFile:
					if len(v) != 1 || (v[0] != fileValuePath && v[0] != fileValueAtPath) {
						t.Errorf("%s --%s: %s = %q, want %q or %q", c.CommandPath(), f.Name, k, v, fileValuePath, fileValueAtPath)
					}
				case annotationArgumentFlag:
					if len(v) != 1 || v[0] != "true" {
						t.Errorf("%s --%s: %s = %q, want \"true\"", c.CommandPath(), f.Name, k, v)
					}
				case annotationWritesTo:
					var target *pflag.Flag
					if len(v) == 1 {
						target = lookupOwnFlag(c, v[0])
					}
					if target == nil || len(target.Annotations[annotationFile]) == 0 {
						t.Errorf("%s --%s: %s = %q, which is not a file flag of this command", c.CommandPath(), f.Name, k, v)
					}
				}
			}
		})
	})
}

// TestInputWithoutArgsIsDeclared holds markInputWithoutArgs both ways: a
// command run with no argument and no file reads stdin if and only if it is
// declared to. For a declared command it also holds markArgumentFlag: a flag
// stands in for the argument when, given it, the command reads no stdin and
// still goes on to call the platform. A flag that only fails validation early
// (--output bogus) calls nothing, so it is not mistaken for one.
func TestInputWithoutArgsIsDeclared(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, `{"error":{"code":400,"message":"input probe"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	isolateProbeHost(t)

	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	walkCommands(root, func(c *cobra.Command) {
		if !c.Runnable() || c == root || c.ValidateArgs(nil) != nil {
			return
		}
		path := commandPathArgs(c)
		stdin := &recordingReader{r: strings.NewReader("fetch logs")}
		argv := append(append(path, fileFlagProbeHints[strings.Join(path, " ")]...), "--agent")
		runProbe(srv.URL, argv, &recordingFS{}, stdin)
		declared := c.Annotations[annotationInputWithoutArgs] == "true"
		switch {
		case stdin.read.Load() && !declared:
			t.Errorf("%s reads stdin without arguments but is not declared; add markInputWithoutArgs to its constructor",
				c.CommandPath())
		case !stdin.read.Load() && declared:
			t.Errorf("%s is declared to read stdin without arguments but did not", c.CommandPath())
		}
		if !declared {
			return
		}
		for name, f := range commandFlags(root, c) {
			if !isStringFlag(f) {
				continue
			}
			stdin := &recordingReader{r: strings.NewReader("fetch logs")}
			requests.Store(0)
			// --name=value: a flag with an optional value would take a separate
			// token as the command's argument instead.
			runProbe(srv.URL, append(append([]string{}, argv...), "--"+name+"=fetch logs"), &recordingFS{}, stdin)
			standsIn := !stdin.read.Load() && requests.Load() > 0
			marked := len(f.Annotations[annotationArgumentFlag]) > 0
			switch {
			case standsIn && !marked:
				t.Errorf("%s --%s stands in for the argument (no stdin read, the command ran) but is not declared; "+
					"add markArgumentFlag(c, %q) to its constructor", c.CommandPath(), name, name)
			case !standsIn && marked:
				t.Errorf("%s --%s is declared to stand in for the argument, but did not", c.CommandPath(), name)
			}
		}
	})
}

// TestVFSEnvCallSitesAreKnown pins the places cmd/ hands the invocation's
// filesystem to code that reads it. A new one is a new way to read a file
// that TestFileFlagsAreDeclared may not see, so it fails here until it is
// listed, with the flags it reads declared.
func TestVFSEnvCallSitesAreKnown(t *testing.T) {
	known := map[string]string{
		"file_input.go:readFileFlagFrom":                       "the shared implementation",
		"query_input.go:resolveQueryInput":                     "query --file; probed",
		"exec_api.go:readRequestBody":                          "exec api -d @path; probed",
		"diff.go:runDiff":                                      "diff reads via readFileFlag; CompareFiles is never called",
		"exec_preview_processor.go:newExecPreviewProcessorCmd": "exec preview-processor --file; probed",
		"translate_lql_to_dql.go:newTranslateLqlToDqlCmd":      "translate lql-to-dql --file; probed",
		"invocation.go:newDQLExecutor":                         "the executor's ExecuteFromFile is never called from cmd/",
		"invocation.go:newFunctionExecutor":                    "exec function --file and --data; probed",
		"invocation.go:newApplier":                             "apply --write-id writes back to --file",
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	found := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "vfsEnv" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "vfsEnv" {
						site := name + ":" + fn.Name.Name
						found[site] = true
						if _, ok := known[site]; !ok {
							t.Errorf("%s: vfsEnv hands the filesystem to new code in %s; declare the flags it reads and list it here",
								fset.Position(call.Pos()), fn.Name.Name)
						}
					}
				}
				return true
			})
		}
	}
	for site := range known {
		if !found[site] {
			t.Errorf("%s no longer calls vfsEnv: remove its entry", site)
		}
	}
}
