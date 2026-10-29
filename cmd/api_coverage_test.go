package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/commands"
	"github.com/Dynatrace/dtctl/pkg/config"
	resapi "github.com/Dynatrace/dtctl/pkg/resources/api"
)

// TestNativeCoverageNamesRealCommands walks the coverage map against the real
// command tree.
//
// Every entry is advice dtctl gives out loud — in the DTCTL column of
// `dtctl get apis`, in the notice `exec api` prints, and in the suggestions on a
// refusal or a failed call. Advice that does not run teaches a caller that
// dtctl's suggestions are unreliable, which costs more than the entry was worth.
// The map is hand-maintained, so this is the only thing keeping it honest.
func TestNativeCoverageNamesRealCommands(t *testing.T) {
	coverage := resapi.CoveredBasePaths()
	require.NotEmpty(t, coverage)

	for basePath, cov := range coverage {
		require.True(t, strings.HasPrefix(basePath, "/"),
			"coverage key %q must be an API base path", basePath)
		require.NotEmpty(t, cov.Resource, "coverage for %q needs a resource label", basePath)
		require.NotEmpty(t, cov.Command, "coverage for %q needs a command to suggest", basePath)

		argv := strings.Fields(cov.Command)
		require.Equal(t, "dtctl", argv[0],
			"Command must be spelled as a caller would type it, got %q", cov.Command)

		found, rest, err := rootCmd.Find(argv[1:])
		require.NoError(t, err, "coverage for %q suggests %q, which does not resolve",
			basePath, cov.Command)
		require.Empty(t, rest,
			"coverage for %q suggests %q, but %v is not part of the command tree",
			basePath, cov.Command, rest)
		require.False(t, found.Hidden,
			"coverage for %q suggests the hidden command %q", basePath, cov.Command)
		require.True(t, found.Runnable() || found.HasSubCommands(),
			"coverage for %q suggests %q, which cannot be run", basePath, cov.Command)
	}
}

// TestExecAPIIsUnadvertisedButDocumented pins the deliberate middle state between
// cobra's two options.
//
// Visible would put an escape hatch in --help and in the compact catalogs an agent
// bootstraps from, right beside the native commands it must not replace. Fully
// hidden would make it undiscoverable, which pushes a caller who genuinely needs
// it toward something worse — an ad-hoc AppEngine function holding a bearer token.
// So: absent from help and from the browsable catalogs, present in the full one.
func TestExecAPIIsUnadvertisedButDocumented(t *testing.T) {
	execCmd, _, err := rootCmd.Find([]string{"exec"})
	require.NoError(t, err)

	apiCmd, _, err := rootCmd.Find([]string{"exec", "api"})
	require.NoError(t, err)
	require.True(t, apiCmd.Hidden, "the escape hatch must not be advertised in --help")

	var listed []string
	for _, sub := range execCmd.Commands() {
		if !sub.Hidden {
			listed = append(listed, sub.Name())
		}
	}
	require.NotContains(t, listed, "api")

	full := commands.Build(rootCmd)
	require.Contains(t, full.Verbs["exec"].Resources, "api",
		"the full catalog is the one place the escape hatch is documented")

	require.NotContains(t, commands.NewBrief(full).Verbs["exec"].Resources, "api",
		"--brief is the documented agent bootstrap; it must not tempt one here")
	require.NotContains(t, commands.NewMinimal(full).Verbs["exec"].Resources, "api")

	// The guardrail travels with the name: the antipatterns are where an agent is
	// meant to learn `exec api` exists, because there the name arrives with its
	// constraint attached.
	joined := strings.Join(full.Antipatterns, "\n")
	require.Contains(t, joined, "exec api")
	require.Contains(t, joined, "exec function --code",
		"the antipattern must name the worse thing it replaces")
}

// TestNoBuiltinProfileGrantsTheEscapeHatch pins that the presets withhold
// `exec api` by construction rather than by remembering to exclude it.
//
// Profiles are default-deny, so this holds as long as no preset grants the bare
// `exec` verb. A preset that wants one exec subcommand must name it (as "query"
// names `exec analyzer`) — and this test is what fails if someone shortens that
// to `exec` for convenience, which would silently hand a scoped agent profile an
// unrestricted HTTP client.
func TestNoBuiltinProfileGrantsTheEscapeHatch(t *testing.T) {
	names := config.BuiltinProfileNames()
	require.NotEmpty(t, names)

	env := newMockEnvironmentAt(t, "readonly")

	for _, name := range names {
		code, out, errOut := runAPI(t, []string{
			"exec", "api", "/platform/widget/v1/widgets", "--agent",
		}, RunOptions{Env: map[string]string{"DTCTL_PROFILE": name}})

		require.NotZero(t, code, "profile %q must not expose 'exec api'", name)

		var resp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &resp), "profile %q: %s%s", name, out, errOut)
		require.Equal(t, "profile_blocked", resp.Error.Code,
			"profile %q must mask the command on the surface axis, not merely refuse it", name)
	}

	require.Zero(t, env.requestCount(),
		"a masked command must be refused before it can reach the environment")
}

// TestGetAndDescribeAPIsSurviveARestrictedProfile is the other half: discovery is
// a read and belongs in a triage profile, so a preset that grants the whole `get`
// and `describe` subtrees gets the API index with them. Nothing here should have
// to be special-cased — the test exists to notice if `get apis` ever acquires a
// reason to be withheld.
func TestGetAndDescribeAPIsSurviveARestrictedProfile(t *testing.T) {
	newMockEnvironmentAt(t, "readonly")

	code, out, errOut := runAPI(t, []string{"get", "apis"},
		RunOptions{Env: map[string]string{"DTCTL_PROFILE": "investigate"}})

	require.Zero(t, code, errOut)
	require.Contains(t, out, "Widget Service")
}
