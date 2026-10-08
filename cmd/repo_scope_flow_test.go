package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
)

// The repo-scope flow end to end, against a mock environment: discover what a
// checkout runs as, save it with the line discovery printed, and read it back.
// It lives here rather than in test/e2e because every file there runs against
// a live environment; this one has to pin exact queries and exact output.

const (
	flowService      = "SERVICE-0123456789ABCDEF"
	flowProcessGroup = "PROCESS_GROUP-FEDCBA9876543210"
	flowSetCommand   = "dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout"
)

// checkoutRows are the aggregates an environment running the checkout
// workload returns: spans carry the service id, logs only the process group.
var checkoutRows = map[string]string{
	"spans": `{"k8s.namespace.name":"payments","k8s.workload.name":"checkout","service.name":"checkout",` +
		`"dt.entity.service":"` + flowService + `","dt.entity.process_group":"` + flowProcessGroup + `","records":"12400"}`,
	"logs": `{"k8s.namespace.name":"payments","k8s.workload.name":"checkout","service.name":"checkout",` +
		`"dt.entity.process_group":"` + flowProcessGroup + `","records":"88000"}`,
}

// chdirCheckoutRepo is a single-service repository whose go.mod and
// Kubernetes manifest both name it checkout.
func chdirCheckoutRepo(t *testing.T) string {
	t.Helper()
	repo := chdirTestRepo(t, "")
	writeRepoFiles(t, repo, map[string]string{
		"go.mod": "module example.invalid/checkout\n\ngo 1.22\n",
		"deploy/k8s/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout
  namespace: payments
spec:
  template:
    spec:
      containers:
        - name: checkout
          image: registry.example.invalid/checkout:1.4.2
`,
	})
	return repo
}

func writeRepoFiles(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(repo, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// listing is every path under each dir with its mode, and each regular
// file's sha256, so a test can prove a command wrote nothing at all: no new
// file, not the scope file nor a temp file beside it, and no change to one
// that was there.
func listing(t *testing.T, dirs ...string) []string {
	t.Helper()
	var entries []string
	for _, dir := range dirs {
		require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			entry := fmt.Sprintf("%s %s", p, info.Mode())
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				entry += fmt.Sprintf(" %x", sha256.Sum256(data))
			}
			entries = append(entries, entry)
			return nil
		}))
	}
	sort.Strings(entries)
	return entries
}

// The no-write assertions rest on listing: a rewritten or re-permissioned
// file keeps its path, so the path set alone would not see it.
func TestListingSeesChangedFiles(t *testing.T) {
	dir := t.TempDir()
	goMod := filepath.Join(dir, "go.mod")
	require.NoError(t, os.WriteFile(goMod, []byte("module example.invalid/checkout\n"), 0o644))
	before := listing(t, dir)

	require.NoError(t, os.WriteFile(goMod, []byte("module example.invalid/checkout\n// appended\n"), 0o644))
	rewritten := listing(t, dir)
	require.NotEqual(t, before, rewritten, "a rewritten file")

	require.NoError(t, os.Chmod(goMod, 0o600))
	require.NotEqual(t, rewritten, listing(t, dir), "a changed mode")
}

// recordQuery is the text discovery sends for spans or logs when the only
// value is "checkout".
func recordQuery(object string) string {
	return "fetch " + object + ", from:now()-24h, scanLimitGBytes:5\n" +
		`| filter in(k8s.workload.name, array("checkout")) or in(service.name, array("checkout"))` + "\n" +
		"| summarize records = count(), by:{k8s.namespace.name, k8s.workload.name, service.name, dt.entity.service, dt.entity.process_group}\n" +
		"| sort records desc\n" +
		"| limit 200"
}

// discoverReport runs `repo-scope discover --agent` and decodes its report.
func discoverReport(t *testing.T, args ...string) (*reposcope.DiscoveryReport, []string) {
	t.Helper()
	code, stdout, stderr := runRepoScoped(append([]string{"repo-scope", "discover", "--agent"}, args...), RunOptions{})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
	var report reposcope.DiscoveryReport
	resp := agentResult(t, stdout, &report)
	return &report, resp.Context.Suggestions
}

// TestRepoScopeFlow is the first-use path, in a readonly context: discover
// proposes, set saves, and current reads back what was saved.
func TestRepoScopeFlow(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", rowsByObject(checkoutRows))
	repo := chdirCheckoutRepo(t)
	before := listing(t, repo)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, `Sending to 127.0.0.1: "checkout" (compared to k8s.workload.name, service.name, entity.name); nothing else leaves this machine`+"\n", stderr)
	require.Contains(t, stdout, "\n  "+flowSetCommand+"\n")
	// The record queries found the workload, so the entity-name fallbacks
	// were never sent.
	require.Equal(t, []string{recordQuery("spans"), recordQuery("logs")}, srv.queries())
	require.Equal(t, before, listing(t, repo), "discover never writes")

	report, suggestions := discoverReport(t)
	require.Equal(t, reposcope.VerdictMatch, report.Verdict)
	require.Equal(t, []string{"checkout"}, report.Sent)
	require.Len(t, report.Candidates, 1)
	require.Equal(t, flowSetCommand, report.Candidates[0].SetCommand)
	require.Equal(t, "confirm with the user, then: "+flowSetCommand, suggestions[0])
	require.Equal(t, before, listing(t, repo), "discover never writes, in agent mode either")

	// Running the proposed line is the confirmation.
	runSetCommand(t, flowSetCommand)
	code, stdout, stderr = runRepoScoped([]string{"repo-scope", "current"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "checkout  (whole repository · 127.0.0.1)\n", stdout)
	require.Len(t, srv.queries(), 4, "only discover talks to the environment")
}

// The terminal output of discover, from reports the mock environment
// produced, with a fixed duration.
func TestRepoScopeDiscoverHumanOutput(t *testing.T) {
	ledger := `{"k8s.namespace.name":"payments","k8s.workload.name":"checkout-ledger","service.name":"checkout-ledger","records":"40"}`
	for _, tc := range []struct {
		golden  string
		respond func(*http.Request, string) (int, string)
	}{
		{"discover-match", rowsByObject(checkoutRows)},
		{"discover-ambiguous", rowsByObject(map[string]string{"spans": checkoutRows["spans"] + "," + ledger})},
		{"discover-none", rowsByObject(nil)},
		{"discover-partial", func(r *http.Request, query string) (int, string) {
			if strings.HasPrefix(query, "fetch spans") {
				return http.StatusBadRequest, `{"error":{"code":400,"message":"Scan limit exceeded.","details":{"errorType":"SCAN_LIMIT"}}}`
			}
			return rowsByObject(checkoutRows)(r, query)
		}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			newDQLRecorder(t, "readonly", tc.respond)
			chdirCheckoutRepo(t)
			report, _ := discoverReport(t)
			var out strings.Builder
			printRepoScopeDiscovery(&out, report, newRepoScopeProposal(report, nil), 600*time.Millisecond)
			testutil.AssertGolden(t, "repo-scope/"+tc.golden, out.String())
		})
	}
}

// A repository nothing in the environment matches: every query is tried, and
// the agent is pointed at the flags that widen the search.
func TestRepoScopeDiscoverNoMatch(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", rowsByObject(nil))
	chdirCheckoutRepo(t)

	report, suggestions := discoverReport(t)
	require.Len(t, srv.queries(), 4, "the entity-name fallbacks run when the records find nothing")
	require.Equal(t, reposcope.VerdictNone, report.Verdict)
	require.Equal(t, repoScopeNarrowHint, suggestions[0])
}

// --path picks another directory of a monorepo, and --term adds a name; both
// are held to what they may carry.
func TestRepoScopeDiscoverPathAndTerm(t *testing.T) {
	newDQLRecorder(t, "readonly", rowsByObject(nil))
	repo := chdirTestRepo(t, "")
	writeRepoFiles(t, repo, map[string]string{
		"services/ledger/Dockerfile":   "FROM scratch\n",
		"services/checkout/Dockerfile": "FROM scratch\n",
	})

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--path", "services/ledger/", "--term", "billing.engine", "--dry-run", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	plan := agentEnvelope(t, stdout).Result.(map[string]interface{})
	require.Equal(t, map[string]interface{}{"unit": "services/ledger", "sent": "billing-engine, billing.engine, billing_engine, billingengine, ledger"}, plan["details"])

	for flag, value := range map[string]string{
		"--path": "../elsewhere",
		"--term": `ledger" or true or "`,
	} {
		code, stdout, _ = runRepoScoped([]string{"repo-scope", "discover", flag, value, "--dry-run", "--agent"}, RunOptions{})
		require.NotZero(t, code)
		detail := agentError(t, stdout)
		require.Equal(t, "validation_error", detail.Code)
		require.True(t, strings.HasPrefix(detail.Message, flag+": "), detail.Message)
	}
	code, stdout, _ = runRepoScoped([]string{"repo-scope", "discover", "--path", "services/billing", "--dry-run", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
}

// --dry-run is the disclosure: it prints every query and value and sends
// nothing, so it works before there is a token, and without a context at all.
func TestRepoScopeDiscoverDryRunNeedsNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a dry run sent %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	writeIsolatedConfig(t, fmt.Sprintf("current-context: c\ncontexts:\n  - name: c\n    context:\n      environment: %s\n", srv.URL))
	repo := chdirCheckoutRepo(t)
	before := listing(t, repo)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "\n   "+strings.ReplaceAll(recordQuery("spans"), "\n", "\n   ")+"\n")

	code, stdout, stderr = runRepoScoped([]string{"--dry-run", "repo-scope", "discover", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	plan := agentEnvelope(t, stdout).Result.(map[string]interface{})
	require.Equal(t, true, plan["dry_run"])
	payload := plan["payload"].([]interface{})
	require.Len(t, payload, 4)
	require.Equal(t, recordQuery("spans"), payload[0].(map[string]interface{})["dql"])

	t.Setenv("DTCTL_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	code, _, stderr = runRepoScoped([]string{"repo-scope", "discover", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, before, listing(t, repo))
}

// A token that provably cannot read spans is refused before the first query
// leaves. One that cannot read the entity tables still gets the record
// queries' answer; the fallbacks are reported as unreadable, not sent.
func TestRepoScopeDiscoverChecksScopes(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", rowsByObject(nil))
	chdirCheckoutRepo(t)

	withGrantedScopes(t, []string{"storage:logs:read", "storage:entities:read"}, true)
	code, stdout, _ := runRepoScoped([]string{"repo-scope", "discover", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	detail := agentError(t, stdout)
	require.Equal(t, "insufficient_scope", detail.Code)
	require.Equal(t, []string{"storage:spans:read"}, detail.MissingScopes)
	require.Empty(t, srv.queries())

	withGrantedScopes(t, []string{"storage:logs:read", "storage:spans:read"}, true)
	report, _ := discoverReport(t)
	require.Equal(t, []string{recordQuery("spans"), recordQuery("logs")}, srv.queries())
	require.Equal(t, []string{"auth", "auth"}, []string{report.Queries[2].Cause, report.Queries[3].Cause})
	require.True(t, report.Partial)
}

// A run that outlives its deadline reports the queries that ran out of time,
// through the real executor, and still exits 0 with what it found.
func TestRepoScopeDiscoverTimeout(t *testing.T) {
	newDQLRecorder(t, "readonly", func(r *http.Request, _ string) (int, string) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`
	})
	chdirCheckoutRepo(t)
	prev := repoScopeDiscoverTimeout
	repoScopeDiscoverTimeout = 200 * time.Millisecond
	t.Cleanup(func() { repoScopeDiscoverTimeout = prev })

	report, _ := discoverReport(t)
	require.Equal(t, "timeout", report.Queries[0].Cause)
	require.True(t, report.Queries[1].Skipped)
	require.True(t, report.Partial)
}

// Discovery keeps no state: it writes nothing to the repository, the config
// directory or any XDG directory.
func TestRepoScopeDiscoverKeepsNoState(t *testing.T) {
	newDQLRecorder(t, "readonly", rowsByObject(checkoutRows))
	repo := chdirCheckoutRepo(t)
	dirs := []string{repo, filepath.Dir(os.Getenv("DTCTL_CONFIG"))}
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		dir := t.TempDir()
		t.Setenv(v, dir)
		dirs = append(dirs, dir)
	}
	before := listing(t, dirs...)

	discoverReport(t)
	code, _, stderr := runRepoScoped([]string{"repo-scope", "discover"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, before, listing(t, dirs...))
}

// Entries are filed under the environment host, so one file serves every
// context and each context sees only its own.
func TestRepoScopePerEnvironment(t *testing.T) {
	writeIsolatedConfig(t, `current-context: prod
contexts:
  - name: prod
    context:
      environment: https://abc12345.apps.dynatrace.com
  - name: staging
    context:
      environment: https://stg98765.apps.dynatrace.com
`)
	repo := chdirTestRepo(t, "")

	code, _, stderr := runRepoScoped([]string{"--context", "staging", "repo-scope", "set", "checkout", "--service-name", "checkout-stg"}, RunOptions{})
	require.Zero(t, code, stderr)
	code, _, stderr = runRepoScoped([]string{"repo-scope", "set", "checkout", "--service-name", "checkout"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"abc12345.apps.dynatrace.com", "stg98765.apps.dynatrace.com"}, loadScopeFile(t, repo).Hosts())

	for _, tc := range []struct {
		opts RunOptions
		want string
	}{
		{RunOptions{}, `service.name == "checkout"`},
		{RunOptions{Env: map[string]string{"DTCTL_CONTEXT": "staging"}}, `service.name == "checkout-stg"`},
	} {
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", "describe", "-o", "json"}, tc.opts)
		require.Zero(t, code, stderr)
		var s reposcope.Status
		require.NoError(t, json.Unmarshal([]byte(stdout), &s))
		require.Equal(t, tc.want, s.Filters["spans"])
	}

	code, _, stderr = runRepoScoped([]string{"--context", "staging", "repo-scope", "delete", "checkout"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"abc12345.apps.dynatrace.com"}, loadScopeFile(t, repo).Hosts())
}

// runSetCommand runs a set line discovery proposed, exactly as given: split
// into words the way a shell reads it.
func runSetCommand(t *testing.T, line string, extra ...string) {
	t.Helper()
	words := shellWords(t, line)
	require.Equal(t, "dtctl", words[0], line)
	code, stdout, stderr := runRepoScoped(append(words[1:], extra...), RunOptions{})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
}

// shellWords splits line as a POSIX shell does for the quoting
// exec.ShellQuote writes: single quotes, and a backslash outside them.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	var words []string
	var word strings.Builder
	inWord, quoted, escaped := false, false, false
	for _, r := range line {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case quoted:
			if r == '\'' {
				quoted = false
			} else {
				word.WriteRune(r)
			}
		case r == '\'':
			quoted, inWord = true, true
		case r == '\\':
			escaped, inWord = true, true
		case r == ' ':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	require.False(t, quoted || escaped, "unterminated quoting in %s", line)
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// The set line saves under the environment discovery asked, however its
// context was chosen: run as given, with the config's current context
// pointing elsewhere, it must not file the candidate under that one.
func TestRepoScopeDiscoverKeepsTheContext(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", rowsByObject(checkoutRows))
	cfg := fmt.Sprintf(`current-context: prod
contexts:
  - name: prod
    context:
      environment: https://abc12345.apps.dynatrace.com
      token-ref: t
  - name: staging
    context:
      environment: %[1]s
      token-ref: t
      safety-level: readonly
  - name: payments staging
    context:
      environment: %[1]s
      token-ref: t
      safety-level: readonly
tokens:
  - name: t
    token: dt0c01.EXAMPLE
`, srv.URL)
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		// withConfig names the config with --config as well.
		withConfig bool
		want       string
	}{
		{name: "--context", args: []string{"--context", "staging"}, want: "--context staging"},
		{name: "DTCTL_CONTEXT", env: map[string]string{"DTCTL_CONTEXT": "staging"}, want: "--context staging"},
		{name: "--config", args: []string{"--context", "staging"}, withConfig: true, want: "--context staging"},
		{name: "a context name a shell splits", args: []string{"--context", "payments staging"}, want: "--context 'payments staging'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeIsolatedConfig(t, cfg)
			configPath := os.Getenv("DTCTL_CONFIG")
			repo := chdirCheckoutRepo(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			args, want := tc.args, "dtctl "
			if tc.withConfig {
				args = append(args, "--config", configPath)
				want += "--config " + exec.ShellQuote(configPath) + " "
			}
			want += tc.want + " repo-scope set "
			report, _ := discoverReport(t, args...)
			line := report.Candidates[0].SetCommand
			require.True(t, strings.HasPrefix(line, want), line)

			// A shell without the selection, and a config that no longer
			// points at the current context through the environment.
			t.Setenv("DTCTL_CONTEXT", "")
			if tc.withConfig {
				t.Setenv("DTCTL_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
			}
			runSetCommand(t, line)
			require.Equal(t, []string{repoScopeTestHost}, loadScopeFile(t, repo).Hosts())
		})
	}

	// Positive control: without a selection the line names none.
	writeIsolatedConfig(t, strings.Replace(cfg, "current-context: prod", "current-context: staging", 1))
	chdirCheckoutRepo(t)
	report, _ := discoverReport(t)
	require.Equal(t, flowSetCommand, report.Candidates[0].SetCommand)
}

// Two namespaces, and a canary beside the main workload, run the same service
// name. The set line for each binds its workload and leaves the service name
// out, since that would select the others' records too, and says so; a
// candidate no workload binding describes falls back to its entity ids.
func TestRepoScopeDiscoverSharedServiceName(t *testing.T) {
	const vmService = "SERVICE-00000000000000AA"
	rows := map[string]string{"spans": `{"k8s.namespace.name":"payments","k8s.workload.name":"checkout","service.name":"checkout","records":"900"},` +
		`{"k8s.namespace.name":"staging","k8s.workload.name":"checkout","service.name":"checkout","records":"500"},` +
		`{"service.name":"checkout","dt.entity.service":"` + vmService + `","records":"100"},` +
		`{"k8s.namespace.name":"payments","k8s.workload.name":"checkout-canary","service.name":"checkout","records":"50"}`}
	newDQLRecorder(t, "readonly", rowsByObject(rows))
	chdirCheckoutRepo(t)

	report, suggestions := discoverReport(t)
	require.Equal(t, reposcope.VerdictAmbiguous, report.Verdict)
	var lines []string
	for _, c := range report.Candidates {
		require.True(t, c.ServiceNameShared)
		lines = append(lines, c.SetCommand)
	}
	require.Equal(t, []string{
		"dtctl repo-scope set checkout --namespace payments --workload checkout",
		"dtctl repo-scope set checkout --namespace staging --workload checkout",
		"dtctl repo-scope set checkout --service " + vmService,
		"dtctl repo-scope set checkout-canary --namespace payments --workload checkout-canary",
	}, lines)
	notes := repoScopeSharedNameNotes(report)
	require.Len(t, notes, 1, "one note for the one shared name")
	require.Contains(t, suggestions, notes[0])

	var out strings.Builder
	printRepoScopeDiscovery(&out, report, newRepoScopeProposal(report, nil), time.Second)
	require.Equal(t, 1, strings.Count(out.String(), "Note: "+notes[0]+"\n"), out.String())

	// The saved entry selects payments alone.
	runSetCommand(t, lines[0])
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "describe", "-o", "json"}, RunOptions{})
	require.Zero(t, code, stderr)
	var s reposcope.Status
	require.NoError(t, json.Unmarshal([]byte(stdout), &s))
	require.Equal(t, `(k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`, s.Filters["logs"])
}

// A monorepo with one build file at the root: every service directory is in
// the root's unit. Discovery run for one of them proposes an entry for that
// directory, not the whole repository, so the service beside it gets its own.
func TestRepoScopeDiscoverRootUnitSubdirectory(t *testing.T) {
	found := true
	newDQLRecorder(t, "readonly", func(_ *http.Request, q string) (int, string) {
		if !found || !strings.HasPrefix(q, "fetch spans") && !strings.HasPrefix(q, "fetch logs") {
			return http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`
		}
		name := "checkout"
		if strings.Contains(q, `"ledger"`) {
			name = "ledger"
		}
		return http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[{"service.name":"` + name + `","records":"100"}]}}`
	})
	repo := chdirTestRepo(t, "")
	writeRepoFiles(t, repo, map[string]string{
		"go.mod":                    "module example.invalid/platform\n\ngo 1.22\n",
		"services/ledger/main.go":   "package main\n",
		"services/checkout/main.go": "package main\n",
	})

	t.Chdir(filepath.Join(repo, "services", "ledger"))
	report, _ := discoverReport(t)
	require.Equal(t, ".", report.Unit)
	require.Equal(t, "services/ledger", report.Dir)
	require.Equal(t, "dtctl repo-scope set ledger --service-name ledger --path services/ledger", report.Candidates[0].SetCommand)
	runSetCommand(t, report.Candidates[0].SetCommand)

	// --path from the root proposes the same, for the other service.
	t.Chdir(repo)
	report, _ = discoverReport(t, "--path", "services/checkout")
	require.Equal(t, "dtctl repo-scope set checkout --service-name checkout --path services/checkout", report.Candidates[0].SetCommand)
	runSetCommand(t, report.Candidates[0].SetCommand)

	for dir, want := range map[string]string{"services/ledger": "ledger", "services/checkout": "checkout"} {
		t.Chdir(filepath.Join(repo, filepath.FromSlash(dir)))
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", "current"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, want+"  ("+dir+" · 127.0.0.1)\n", stdout)
	}

	// Positive control: from the root itself the entry covers the whole
	// repository.
	t.Chdir(repo)
	report, _ = discoverReport(t, "--term", "ledger")
	require.NotContains(t, report.Candidates[0].SetCommand, "--path")

	// Nothing found for the directory: no candidate's name ties it to the
	// lines for linking by hand, so they carry the context discovery ran in
	// but no --path, and a separate line says how to cover the directory.
	found = false
	t.Chdir(filepath.Join(repo, "services", "ledger"))
	report, suggestions := discoverReport(t, "--context", "c")
	require.Equal(t, reposcope.VerdictNone, report.Verdict)
	require.Equal(t, repoScopeNarrowHint, suggestions[0])
	require.True(t, strings.HasPrefix(suggestions[1], "link by hand: dtctl --context c repo-scope set <name> "), suggestions[1])
	require.NotContains(t, suggestions[1], "--path")
	require.Contains(t, suggestions[2], "--path services/ledger")

	code, stdout, stderr := runRepoScoped([]string{"--context", "c", "repo-scope", "discover", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	var links []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "  dtctl ") {
			links = append(links, line)
		}
	}
	require.Len(t, links, 3, stdout)
	for _, l := range links {
		require.True(t, strings.HasPrefix(l, "  dtctl --context c repo-scope set <name> "), l)
		require.NotContains(t, l, "--path", l)
	}
	require.Contains(t, stdout, "--path services/ledger")
}

// A single-module repository: go.mod at the root names the service, and a
// package directory below it is not one. Discovery run from that directory
// still sends its name, but the candidate it finds is what go.mod and the
// manifest named, so the entry covers the whole repository.
func TestRepoScopeDiscoverSingleModuleSubdirectory(t *testing.T) {
	newDQLRecorder(t, "readonly", rowsByObject(checkoutRows))
	repo := chdirCheckoutRepo(t)
	writeRepoFiles(t, repo, map[string]string{"internal/handlers/handlers.go": "package handlers\n"})
	t.Chdir(filepath.Join(repo, "internal", "handlers"))

	report, _ := discoverReport(t)
	require.Equal(t, ".", report.Unit)
	require.Equal(t, "internal/handlers", report.Dir)
	require.Contains(t, report.Sent, "handlers", "the directory's name is looked for")
	require.False(t, report.Candidates[0].MatchedDir)
	require.Equal(t, flowSetCommand, report.Candidates[0].SetCommand)

	runSetCommand(t, report.Candidates[0].SetCommand)
	t.Chdir(repo)
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "current"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "checkout  (whole repository · 127.0.0.1)\n", stdout)
}

// A match no binding selects alone, a workload whose records carry no
// namespace, service name or entity id, has no line to save it. Discovery
// offers what it offers when nothing matched, never an empty line.
func TestRepoScopeDiscoverMatchWithoutABinding(t *testing.T) {
	newDQLRecorder(t, "readonly", rowsByObject(map[string]string{"spans": `{"k8s.workload.name":"checkout","records":"10"}`}))
	chdirCheckoutRepo(t)

	report, suggestions := discoverReport(t)
	require.Equal(t, reposcope.VerdictMatch, report.Verdict)
	require.Len(t, report.Candidates, 1)
	require.Empty(t, report.Candidates[0].SetCommand)
	require.Equal(t, repoScopeNarrowHint, suggestions[0])
	require.True(t, strings.HasPrefix(suggestions[1], "link by hand: dtctl repo-scope set <name> "), suggestions[1])
	for _, s := range suggestions {
		require.False(t, strings.HasPrefix(s, "confirm with the user"), s)
	}

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.NotContains(t, stdout, "Save it with")
	require.NotContains(t, stdout, "\n  \n", "no empty line where a set line would be")
	require.Contains(t, stdout, "\n  dtctl repo-scope set <name> --namespace <namespace> --workload <workload>\n")
}

// Two namespaces report one service name and no workload, so no binding
// selects either candidate alone. Discovery says so and offers the one line
// there is, the service name, as selecting both.
func TestRepoScopeDiscoverNothingTellsCandidatesApart(t *testing.T) {
	const line = "dtctl repo-scope set checkout --service-name checkout"
	newDQLRecorder(t, "readonly", rowsByObject(map[string]string{"spans": `{"k8s.namespace.name":"payments","service.name":"checkout","records":"90"},` +
		`{"k8s.namespace.name":"staging","service.name":"checkout","records":"80"}`}))
	repo := chdirCheckoutRepo(t)

	report, suggestions := discoverReport(t)
	require.Equal(t, reposcope.VerdictAmbiguous, report.Verdict)
	require.Len(t, report.Candidates, 2)
	for _, c := range report.Candidates {
		require.Empty(t, c.SetCommand)
	}
	require.True(t, strings.HasSuffix(suggestions[0], ": "+line), suggestions[0])
	notes := repoScopeSharedNameNotes(report)
	require.Len(t, notes, 1)
	require.NotContains(t, suggestions, notes[0], "there is no set line to leave the name out of")

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, ":\n  "+line+"\n")
	require.NotContains(t, stdout, "no binding selects this candidate alone")
	require.NotContains(t, stdout, notes[0])

	runSetCommand(t, line)
	require.Equal(t, []string{"checkout"}, loadScopeFile(t, repo).Entries(repoScopeTestHost)[0].ServiceNames)
}
