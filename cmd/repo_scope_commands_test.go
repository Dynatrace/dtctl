package cmd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// --path names a directory below the root. The root itself is the entry
// without one, and the advice is about the flag, not about the file's key.
func TestRepoScopeSetRefusesTheRootAsAPath(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")
	code, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "all", "--service-name", "all", "--path", ".", "--agent"}, RunOptions{})
	require.Equal(t, client.ExitUsageError, code)
	detail := agentError(t, stdout)
	require.Equal(t, `--path: "." is the repository root, which an entry without a path covers`, detail.Message)
	require.Equal(t, []string{"leave --path out for an entry that covers the whole repository"}, detail.Suggestions)
	require.NoFileExists(t, filepath.Join(repo, reposcope.FileName))
}
