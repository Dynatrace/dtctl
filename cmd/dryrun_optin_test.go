package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

// TestDryRunIsRejectedWhereNotImplemented covers #477: --dry-run used to be a
// global flag, so a command that ignored it did the real work. A command that
// does not implement it must now reject it as an unknown flag.
func TestDryRunIsRejectedWhereNotImplemented(t *testing.T) {
	for name, args := range map[string][]string{
		"exec workflow":       {"exec", "workflow", "wf-1", "--dry-run"},
		"exec function":       {"exec", "function", "app/fn", "--dry-run"},
		"get slos":            {"get", "slos", "--dry-run"},
		"query":               {"query", "fetch logs", "--dry-run"},
		"before the verb":     {"--dry-run", "exec", "workflow", "wf-1"},
		"before the resource": {"exec", "--dry-run", "workflow", "wf-1"},
	} {
		t.Run(name, func(t *testing.T) {
			// The typed error comes from the per-command FlagErrorFunc, which
			// only executeArgs installs. Without this the test passes or fails
			// on whether some earlier test in the package happened to run an
			// invocation.
			setupErrorHandlers(rootCmd)
			// SetArgs(nil) would send the next Execute() back to os.Args —
			// the test binary's own flags.
			t.Cleanup(func() { rootCmd.SetArgs([]string{}) })
			rootCmd.SetArgs(args)

			err := rootCmd.Execute()

			require.Error(t, err)
			require.Contains(t, err.Error(), "unknown flag --dry-run")
			require.Equal(t, client.ExitUsageError, exitCodeForError(err))
		})
	}
}

// TestDryRunFlagFollowsSharedRunE pins that commands running the same function
// agree on whether they take --dry-run. `share dashboard` is `share document`
// down to the RunE pointer, dry-run branch included, so registering the flag on
// one and not the other made the same implementation reject a flag it honors.
func TestDryRunFlagFollowsSharedRunE(t *testing.T) {
	// installScopePreflight wraps every RunE in one shared function literal,
	// and reflect reports a closure by its code pointer — so on a tree an
	// earlier test has executed, every command would look like the same
	// implementation. Compare the as-registered functions instead.
	restorePristineTree(context.Background())

	byImpl := map[uintptr][]*cobra.Command{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.RunE != nil {
			key := reflect.ValueOf(c.RunE).Pointer()
			byImpl[key] = append(byImpl[key], c)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	hasOwnDryRun := func(c *cobra.Command) bool {
		f := c.Flags().Lookup("dry-run")
		return f != nil && f != rootDryRunFlag
	}

	for _, group := range byImpl {
		if len(group) < 2 {
			continue
		}
		want := hasOwnDryRun(group[0])
		for _, c := range group[1:] {
			require.Equalf(t, want, hasOwnDryRun(c),
				"%q and %q share a RunE but disagree on --dry-run",
				group[0].CommandPath(), c.CommandPath())
		}
	}
}

// TestRejectedFlagExitsWithUsageCode covers the whole invocation, not just the
// error value: the process exit code must be 2. A live run showed 1, because
// the top-level handler re-ran the text-matching flag enhancer over an error a
// subcommand had already typed, which flattened it back to a plain error.
func TestRejectedFlagExitsWithUsageCode(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "workflow", "wf-1", "--dry-run", "--plain"},
		{"get", "dashboards", "--format", "json", "--plain"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			out := captureStdout(t, func() {
				require.Equal(t, client.ExitUsageError, Run(args, RunOptions{}))
			})

			// The parse failed before --plain reached its flag variable, so the
			// envelope has to come from the raw arguments.
			require.Contains(t, out, `"ok":false`)
		})
	}
}

// TestDeleteDryRunSendsNoMutatingRequest runs delete commands with --dry-run
// against a mock environment whose writes fail, and asserts on the traffic.
func TestDeleteDryRunSendsNoMutatingRequest(t *testing.T) {
	srv := newCloudMockServer(t)

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"delete aws connection", deleteAWSConnectionCmd, []string{mockAWSConnectionName}},
		{"delete aws monitoring", deleteAWSMonitoringConfigCmd, []string{mockAWSConfigName}},
		{"delete azure connection", deleteAzureConnectionCmd, []string{mockAzureConnectionName}},
		{"delete azure monitoring", deleteAzureMonitoringConfigCmd, []string{mockAzureConfigName}},
		{"delete gcp connection", deleteGCPConnectionCmd, []string{mockGCPConnectionName}},
		{"delete gcp monitoring", deleteGCPMonitoringConfigCmd, []string{mockGCPConfigName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupPlatformCmdTest(t, srv.Server, "json")
			origDryRun := dryRun(context.Background())
			t.Cleanup(func() { gFlags.dryRun = origDryRun })
			gFlags.dryRun = true
			srv.reset()

			var runErr error
			out := capturePlatformStdout(t, func() {
				runErr = tc.cmd.RunE(tc.cmd, tc.args)
			})

			require.NoError(t, runErr, "output:\n%s", out)
			require.Contains(t, out, "Dry run: would delete")
			require.Empty(t, srv.mutatingCalls())
		})
	}
}

func TestDeleteSLODryRunSendsNoMutatingRequest(t *testing.T) {
	var mutating []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutating = append(mutating, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "slo-1", "name": "Availability", "version": "3"})
	}))
	t.Cleanup(srv.Close)
	setupPlatformCmdTest(t, srv, "json")
	origDryRun := dryRun(context.Background())
	t.Cleanup(func() { gFlags.dryRun = origDryRun })
	gFlags.dryRun = true

	var runErr error
	out := capturePlatformStdout(t, func() {
		runErr = deleteSLOCmd.RunE(deleteSLOCmd, []string{"slo-1"})
	})

	require.NoError(t, runErr, "output:\n%s", out)
	require.Contains(t, out, `Dry run: would delete SLO "Availability" (slo-1)`)
	require.Empty(t, mutating)
}

// TestRejectionSuggestsVerifyOnlyWhenItExists pins that the rejection points at
// a `dtctl verify` subcommand that is really there. The first wording sent
// every command to "dtctl verify", which has nothing for a workflow or a login,
// so the advice cost the caller a second failed command to disprove.
func TestRejectionSuggestsVerifyOnlyWhenItExists(t *testing.T) {
	restorePristineTree(context.Background())

	find := func(path ...string) *cobra.Command {
		c, _, err := rootCmd.Find(path)
		require.NoError(t, err)
		require.Equal(t, path[len(path)-1], c.Name())
		return c
	}

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		want string
	}{
		{"query has a verify", find("query"), "dtctl verify query"},
		{"exec analyzer has a verify", find("exec", "analyzer"), "dtctl verify analyzer"},
		{"exec workflow has none", find("exec", "workflow"), ""},
		{"auth login has none", find("auth", "login"), ""},
		{"verify query is not its own alternative", find("verify", "query"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, verifyAlternativeFor(tc.cmd))

			msg := dryRunUnavailableMessage(tc.cmd)
			require.Contains(t, msg, "has no dry run")
			// Assert on the suggestion clause, not on the string "dtctl
			// verify": the command path of `verify query` contains it either
			// way, and what matters is whether advice was appended at all.
			if tc.want == "" {
				require.NotContains(t, msg, "without running it")
			} else {
				require.Contains(t, msg, "use '"+tc.want+"'")
			}
		})
	}

	// A per-invocation tree must look up its own verify, not the singleton's.
	fresh := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	for path, want := range map[string]string{"verify query": "", "query": "dtctl verify query"} {
		c, _, err := fresh.Find(strings.Fields(path))
		require.NoError(t, err)
		require.Equal(t, want, verifyAlternativeFor(c), "fresh tree: %s", path)
	}
}
