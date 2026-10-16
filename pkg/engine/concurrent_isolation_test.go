package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/engine"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// The gates for concurrent execution (docs/dev/CONCURRENT_EXECUTION.md).
//
// The serialized engine is the reference: TestEngineOutputEqualsCLI holds it
// byte-identical to the real binary. A concurrent engine runs every command
// on a per-invocation tree, so each test here asks whether that tree, and the
// per-goroutine seams around it, behave exactly like the serialized path while
// many invocations overlap — across commands whose wiring used to live only in
// init() on the singleton (dry runs, document and share flags, get's
// --limit/--fields), and across the stream and flag seams (agent-mode query
// output, --jq promotion, cobra's own help writer).

// recordedRequest is one request a tenantEnv received.
type recordedRequest struct {
	method, path, auth string
}

// tenantEnv is a stand-in Dynatrace environment with enough surface for the
// corpus below. It records every request so a test can tell whether a dry run
// wrote anything and whose token reached it.
type tenantEnv struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recordedRequest
}

func newTenantEnv(t testing.TB) *tenantEnv {
	t.Helper()
	env := &tenantEnv{}
	env.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		env.reqs = append(env.reqs, recordedRequest{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		env.mu.Unlock()

		// A little latency so overlapping invocations genuinely overlap.
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/platform/storage/management/v1/bucket-definitions":
			_, _ = io.WriteString(w, `{"buckets":[{"bucketName":"alpha_bucket","table":"logs","status":"active","retentionDays":35,"version":1,"updatable":true},{"bucketName":"beta_bucket","table":"spans","status":"active","retentionDays":10,"version":2,"updatable":true}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/platform/storage/management/v1/bucket-definitions/alpha_bucket":
			_, _ = io.WriteString(w, `{"bucketName":"alpha_bucket","table":"logs","status":"active","retentionDays":35,"version":1,"updatable":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/platform/automation/v1/workflows":
			_, _ = io.WriteString(w, `{"count":2,"results":[{"id":"11111111-2222-4333-8444-555555555555","title":"alpha flow","owner":"owner-1","isPrivate":false},{"id":"wf-2","title":"beta flow","owner":"owner-2","isPrivate":true}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/platform/automation/v1/workflows/11111111-2222-4333-8444-555555555555":
			_, _ = io.WriteString(w, `{"id":"11111111-2222-4333-8444-555555555555","title":"alpha flow","owner":"owner-1","isPrivate":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/platform/document/v1/documents":
			_, _ = io.WriteString(w, `{"documents":[{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","name":"Dash One","type":"dashboard","owner":"owner-1","version":3},{"id":"doc-2","name":"Notes","type":"notebook","owner":"owner-2","version":1}],"totalCount":2}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/platform/document/v1/documents/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"):
			_, _ = io.WriteString(w, `{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","name":"Dash One","type":"dashboard","owner":"owner-1","version":3}`)
		case r.Method == http.MethodPost && r.URL.Path == "/platform/storage/query/v1/query:execute":
			_, _ = io.WriteString(w, `{"state":"SUCCEEDED","result":{"records":[{"host":"h1","n":1},{"host":"h2","n":2}],"types":[],"metadata":{}}}`)
		case r.Method != http.MethodGet:
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
		}
	}))
	t.Cleanup(env.Close)
	return env
}

// writes returns the requests that could have changed the tenant: anything
// but a GET, except running a query.
func (e *tenantEnv) writes() []recordedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []recordedRequest
	for _, r := range e.reqs {
		if r.method != http.MethodGet && !strings.HasPrefix(r.path, "/platform/storage/query/") {
			out = append(out, r)
		}
	}
	return out
}

func (e *tenantEnv) authHeaders() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]int{}
	for _, r := range e.reqs {
		out[r.auth]++
	}
	return out
}

func tenantRequest(url, token, command string) engine.Request {
	return engine.Request{
		Command:        command,
		EnvironmentURL: url,
		Token:          token,
		// Permissive, so that a dry run the engine failed to honour would
		// reach the environment as a real write instead of a safety refusal.
		SafetyLevel: "dangerously-unrestricted",
	}
}

// floors are the stability floors the corpus runs at: the session default
// (stable), where experimental flags are refused, and experimental, where
// they run — so both the refusal and the flags themselves are compared.
var floors = []string{"", "experimental"}

func floorRequest(url, token, command, floor string) engine.Request {
	r := tenantRequest(url, token, command)
	r.MinStability = floor
	return r
}

// isolationCorpus covers the wiring and seams the per-invocation tree has to
// reproduce. Each entry names what it guards.
var isolationCorpus = []string{
	"get workflows --agent",                    // baseline
	"get workflows --agent --limit 1",          // a subcommand's own server-side --limit
	"get buckets --agent --fields bucketName",  // get-wide --fields (registered in init, not in a constructor)
	"get buckets --agent",                      // agent-mode default page through the get-wide --limit
	"get dashboards --agent --name Dash",       // document list flags (init-only)
	"get documents --agent --type dashboard",   // ditto, with --type
	"get buckets --jq .[0].bucketName",         // --jq promotes table output to json in PersistentPreRunE
	"query 'fetch logs' --agent",               // DQL envelope
	"query 'fetch logs' --agent --jq .records", // the --jq envelope pkg/exec wrote to os.Stdout
	"query 'fetch logs' --agent -o json",       // an explicit -o in agent mode wins over auto
	"commands --agent",                         // catalog of this invocation's masked tree
	"commands --brief --agent",                 // a command-specific flag
	"get --help",                               // cobra's own help writer
	"get gcp connections --agent",              // preview notice (a per-run map)
	"create workflow --agent",                  // required --file
	"get workflows --agent --no-such-flag",     // unknown-flag error path
	"delete workflow 11111111-2222-4333-8444-555555555555 --dry-run --agent",    // dry run (init-only)
	"delete bucket alpha_bucket --dry-run",                                      // dry run outside agent mode
	"share dashboard aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee --user u-1 --dry-run", // share flags and dry run
	"version --agent",
}

// TestConcurrentEqualsSerialized is the tree-factory gate: every command in the corpus,
// run many times over with other commands in flight, produces exactly what the
// serialized engine produces — and nothing reaches the host's own streams.
//
// Each reference comes from a process of its own (pristineReference). The
// serialized engine's singleton tree carries state from one request to the
// next — cobra adds a command's --help flag the first time it runs, and merges
// a parent's persistent flags into a child's for good — so a reference taken
// after other requests depends on which ones ran. One measurable consequence:
// once `get buckets` has run, the session's stable floor no longer refuses the
// experimental `get buckets --fields`. A per-invocation tree has no history,
// so the only faithful reference is a process that has none either.
func TestConcurrentEqualsSerialized(t *testing.T) {
	env := newTenantEnv(t)
	const token = "dt0c01.TENANT.REFERENCE"

	type key struct{ command, floor string }
	want := make(map[key]*engine.Result, len(isolationCorpus)*len(floors))
	for _, f := range floors {
		for _, c := range isolationCorpus {
			want[key{c, f}] = pristineReference(t, env.URL, token, c, f)
		}
	}

	const rounds = 4
	total := rounds * len(want)
	eng := engine.New(engine.Limits{MaxQueued: total, MaxDuration: time.Minute}, engine.WithConcurrentExecution(total))

	type outcome struct {
		key
		res *engine.Result
		err error
	}
	results := make(chan outcome, total)
	leaked := captureProcessStreams(t, func() {
		var wg sync.WaitGroup
		for r := 0; r < rounds; r++ {
			for k := range want {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res, err := eng.Execute(context.Background(), floorRequest(env.URL, token, k.command, k.floor))
					results <- outcome{k, res, err}
				}()
			}
		}
		wg.Wait()
	})
	close(results)

	for o := range results {
		assertSameResult(t, fmt.Sprintf("%s [floor %q]", o.command, o.floor), want[o.key], o.res, o.err)
	}
	require.Empty(t, leaked, "a concurrent invocation wrote to the host process's own stdout/stderr")
}

const (
	referenceCommandEnv = "DTCTL_ENGINE_REFERENCE_COMMAND"
	referenceURLEnv     = "DTCTL_ENGINE_REFERENCE_URL"
	referenceTokenEnv   = "DTCTL_ENGINE_REFERENCE_TOKEN"
	referenceFloorEnv   = "DTCTL_ENGINE_REFERENCE_FLOOR"
	referenceMarker     = "ENGINE-REFERENCE:"
)

// pristineReference runs one command through the serialized engine in a fresh
// copy of this test binary, so that no earlier request has touched the
// singleton tree it runs on.
func pristineReference(t *testing.T, url, token, command, floor string) *engine.Result {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSerializedReference$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		referenceCommandEnv+"="+command, referenceURLEnv+"="+url,
		referenceTokenEnv+"="+token, referenceFloorEnv+"="+floor)
	out, err := cmd.Output()
	require.NoError(t, err, "reference process for %q", command)
	for _, line := range strings.Split(string(out), "\n") {
		if payload, ok := strings.CutPrefix(line, referenceMarker); ok {
			var res engine.Result
			require.NoError(t, json.Unmarshal([]byte(payload), &res))
			return &res
		}
	}
	t.Fatalf("reference process for %q printed no result:\n%s", command, out)
	return nil
}

// TestHelperSerializedReference is not a test of its own: pristineReference
// re-executes the test binary to run it in a process nothing else has run in.
func TestHelperSerializedReference(t *testing.T) {
	command := os.Getenv(referenceCommandEnv)
	if command == "" {
		t.Skip("only runs as pristineReference's helper process")
	}
	res, err := engine.Execute(context.Background(),
		floorRequest(os.Getenv(referenceURLEnv), os.Getenv(referenceTokenEnv), command, os.Getenv(referenceFloorEnv)))
	require.NoError(t, err)
	payload, err := json.Marshal(res)
	require.NoError(t, err)
	fmt.Println(referenceMarker + string(payload))
}

// assertSameResult compares one concurrent run against the serialized one,
// naming the command in any mismatch.
func assertSameResult(t *testing.T, command string, want, got *engine.Result, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%q: execute: %v", command, err)
		return
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("%q: exit code %d, serialized %d\nstderr: %s", command, got.ExitCode, want.ExitCode, got.Stderr)
	}
	if g, w := normalizeEnvelope(string(got.Stdout)), normalizeEnvelope(string(want.Stdout)); g != w {
		t.Errorf("%q: stdout differs from serialized\n%s", command, firstDifference(w, g))
	}
	if g, w := normalizeEnvelope(string(got.Stderr)), normalizeEnvelope(string(want.Stderr)); g != w {
		t.Errorf("%q: stderr differs from serialized\n%s", command, firstDifference(w, g))
	}
}

// firstDifference shows where two outputs part ways, with a little context,
// rather than both outputs whole (a catalog runs to hundreds of lines).
func firstDifference(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	i := 0
	for i < len(wl) && i < len(gl) && wl[i] == gl[i] {
		i++
	}
	window := func(lines []string) string {
		lo, hi := max(0, i-2), min(len(lines), i+3)
		return strings.Join(lines[lo:hi], "\n")
	}
	return fmt.Sprintf("first difference at line %d (serialized %d lines, concurrent %d)\n--- serialized\n%s\n--- concurrent\n%s",
		i+1, len(wl), len(gl), window(wl), window(gl))
}

// captureProcessStreams runs fn with the process's stdout and stderr pointed
// at pipes and returns whatever was written to them. A concurrent invocation
// must write only to its own buffers; anything here would, in a service, land
// in the host's logs and be missing from the tenant's response.
func captureProcessStreams(t *testing.T, fn func()) string {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = w, w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		fn()
	}()
	_ = w.Close()
	<-done
	_ = r.Close()
	return buf.String()
}

// TestConcurrentDryRunSendsNoWrite pins the failure that motivated
// TestNewCommandTreeMatchesSingleton: on a per-invocation tree that lacked the
// dry-run commands' own --dry-run, the flag parsed into the hidden root
// declaration, nothing read it, and the command did the real thing.
func TestConcurrentDryRunSendsNoWrite(t *testing.T) {
	env := newTenantEnv(t)
	commands := []string{
		"delete workflow 11111111-2222-4333-8444-555555555555 --dry-run --agent",
		// Without a prompt in the way: on a tree wired by init() these sent the DELETE and
		// reported the workflow deleted.
		"delete workflow 11111111-2222-4333-8444-555555555555 --dry-run --agent --plain",
		"delete workflow 11111111-2222-4333-8444-555555555555 --dry-run --yes --agent",
		"delete bucket alpha_bucket --dry-run --agent",
		"delete dashboard aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee --dry-run --agent",
		"share dashboard aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee --user u-1 --dry-run --agent",
		"unshare dashboard aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee --all --dry-run --agent",
	}
	const rounds = 4
	eng := engine.New(engine.Limits{MaxQueued: 64, MaxDuration: time.Minute}, engine.WithConcurrentExecution(32))

	var wg sync.WaitGroup
	for r := 0; r < rounds; r++ {
		for _, c := range commands {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := eng.Execute(context.Background(), tenantRequest(env.URL, "dt0c01.TENANT.DRYRUN", c))
				if err != nil {
					t.Errorf("%q: %v", c, err)
					return
				}
				if !strings.Contains(string(res.Stdout), "dry") && !strings.Contains(string(res.Stdout), "Dry") {
					t.Errorf("%q: output does not report a dry run (exit %d)\nstdout: %s\nstderr: %s", c, res.ExitCode, res.Stdout, res.Stderr)
				}
			}()
		}
	}
	wg.Wait()

	require.Empty(t, env.writes(), "a --dry-run request reached the environment as a write")
}

// TestConcurrentTenantsStayIsolated runs several tenants, each with its own
// environment and token, through one engine at once: every environment must
// see only its own tenant's token, and every response must carry its own
// tenant's data.
func TestConcurrentTenantsStayIsolated(t *testing.T) {
	const tenants, rounds = 6, 8
	envs := make([]*tenantEnv, tenants)
	for i := range envs {
		envs[i] = newTenantEnv(t)
	}
	token := func(i int) string { return fmt.Sprintf("dt0c01.TENANT%d.ISOLATION", i) }

	eng := engine.New(engine.Limits{MaxQueued: tenants * rounds * 4, MaxDuration: time.Minute}, engine.WithConcurrentExecution(tenants*4))
	var wg sync.WaitGroup
	for r := 0; r < rounds; r++ {
		for i := range envs {
			for _, c := range []string{"get workflows --agent", "query 'fetch logs' --agent", "get dashboards --agent --name Dash"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res, err := eng.Execute(context.Background(), tenantRequest(envs[i].URL, token(i), c))
					if err != nil {
						t.Errorf("tenant %d %q: %v", i, c, err)
						return
					}
					if res.ExitCode != 0 {
						t.Errorf("tenant %d %q: exit %d\nstdout: %s\nstderr: %s", i, c, res.ExitCode, res.Stdout, res.Stderr)
					}
				}()
			}
		}
	}
	wg.Wait()

	for i, env := range envs {
		seen := env.authHeaders()
		require.Len(t, seen, 1, "tenant %d's environment saw other tenants' credentials: %v", i, seen)
		for auth := range seen {
			require.Contains(t, auth, token(i), "tenant %d's environment saw another tenant's token", i)
		}
	}
}

// TestConcurrentRequestDecidesItsOwnSurface is TestExecute_ProfileMasksSurface
// and TestExecute_FloorIsAskedForNotInherited on the concurrent path. There the
// session's scrub and the request's own variables live in an overlay on the
// invocation rather than in the process environment, so anything resolving
// them must read the overlay: a profile the request asked for has to apply,
// and a floor the host set for its own CLI must not.
func TestConcurrentRequestDecidesItsOwnSurface(t *testing.T) {
	t.Setenv(config.MinStabilityEnvVar, "experimental")
	t.Setenv(config.ProfileEnvVar, "full")
	eng := engine.New(engine.Limits{MaxQueued: 4, MaxDuration: time.Minute}, engine.WithConcurrentExecution(4))

	res, err := eng.Execute(context.Background(), engine.Request{
		Command: "inventory --agent", EnvironmentURL: unroutable, Token: "t",
	})
	require.NoError(t, err)
	require.Contains(t, string(res.Stdout), blockedCode,
		"the host's DTCTL_MIN_STABILITY widened a request that asked for the default floor")

	res, err = eng.Execute(context.Background(), engine.Request{
		Command: "get buckets --agent", EnvironmentURL: unroutable, Token: "t", Profile: "query",
	})
	require.NoError(t, err)
	require.Contains(t, string(res.Stdout), `"code":"profile_blocked"`,
		"the request's profile did not apply")
}

// TestConcurrentOutputNeverCarriesColour holds colour to the invocation. The
// output package decides colour once per process, from the host's NO_COLOR and
// FORCE_COLOR and whether the host's stdout is a terminal; the serialized engine
// scrubs the variables and pipes the stream, so the host never reaches a
// response. A concurrent invocation does neither (its scrub is an overlay the
// output package does not read, and its stdout is the caller's writer), so Run
// pins colour off for it instead. Without the pin, a host started from a
// terminal, or with FORCE_COLOR set, coloured every non-agent response.
func TestConcurrentOutputNeverCarriesColour(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	// The decision is cached per process. Start undecided, as a host whose
	// first invocation is concurrent would be; nothing runs while this resets.
	output.ResetColorCache()
	t.Cleanup(output.ResetColorCache)

	env := newTenantEnv(t)
	eng := engine.New(engine.Limits{MaxQueued: 4, MaxDuration: time.Minute}, engine.WithConcurrentExecution(2))
	for _, c := range []string{"get buckets", "get --help"} {
		res, err := eng.Execute(context.Background(), tenantRequest(env.URL, "dt0c01.TENANT.COLOUR", c))
		require.NoError(t, err)
		require.NotContains(t, string(res.Stdout), "\x1b[", "%q: the host's FORCE_COLOR coloured a tenant's response", c)
		require.NotContains(t, string(res.Stderr), "\x1b[", "%q: the host's FORCE_COLOR coloured a tenant's response", c)
	}
}
