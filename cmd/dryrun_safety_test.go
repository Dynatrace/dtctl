package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

// setReadonlyContext downgrades the context setupPlatformCmdTest wrote to
// `readonly`, so a test can assert what a dry run does in the context that
// refuses every mutation.
func setReadonlyContext(t *testing.T) {
	t.Helper()

	cfg, err := LoadConfig()
	require.NoError(t, err)
	ctx, err := cfg.CurrentContextObj()
	require.NoError(t, err)
	cfg.SetContextWithOptions(cfg.CurrentContext, ctx.Environment, "",
		&config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
	require.NoError(t, cfg.SaveTo(cfgFile(context.Background())))

	// Sanity: the level really is refusing deletes, so a passing dry-run
	// assertion below cannot be a misconfigured context quietly permitting one.
	reloaded, err := LoadConfig()
	require.NoError(t, err)
	checker, err := NewSafetyChecker(reloaded)
	require.NoError(t, err)
	require.Error(t, checker.CheckError(safety.OperationDelete, safety.OwnershipUnknown))
}

// TestDryRunNeedsNoSafetyLevel is the invariant CheckSafety's dry-run exemption
// rests on: a preview reads and writes nothing, so it must work in a readonly
// context -- and must still send no mutating request there.
//
// Before this, every delete preview returned the safety refusal instead of the
// preview, which is backwards: readonly is the context whose whole purpose is
// looking without touching.
func TestDryRunNeedsNoSafetyLevel(t *testing.T) {
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
			setReadonlyContext(t)
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

// TestRealRunStillNeedsSafetyLevel is the other half: the exemption must be the
// dry run's alone. The same command, same readonly context, without the flag.
func TestRealRunStillNeedsSafetyLevel(t *testing.T) {
	srv := newCloudMockServer(t)
	setupPlatformCmdTest(t, srv.Server, "json")
	setReadonlyContext(t)
	origDryRun := dryRun(context.Background())
	t.Cleanup(func() { gFlags.dryRun = origDryRun })
	gFlags.dryRun = false
	srv.reset()

	err := deleteAWSConnectionCmd.RunE(deleteAWSConnectionCmd, []string{mockAWSConnectionName})

	require.Error(t, err)
	require.Empty(t, srv.mutatingCalls())
}

// TestSafetyChecksGoThroughCheckSafety keeps the dry-run exemption in one
// place. A command calling checker.CheckError directly reintroduces the split
// this PR closed: the check would run under --dry-run again, and only for that
// command, which is how the ordering drifted to roughly half and half.
func TestSafetyChecksGoThroughCheckSafety(t *testing.T) {
	// exec_api.go reports the verdict *inside* its dry-run output ("Safety:
	// BLOCKED in this context"), so it must consult the real checker while
	// dryRun is set. root.go is where CheckSafety itself lives.
	allowed := map[string]bool{
		"exec_api.go": true,
		"root.go":     true,
	}

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		require.NotContainsf(t, string(src), "checker.CheckError(",
			"%s calls checker.CheckError directly; use CheckSafety so --dry-run "+
				"stays exempt in one place", f)
	}
}
