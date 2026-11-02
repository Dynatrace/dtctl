package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/config"
)

func TestAliasSetAndList(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config")

	// Create initial config
	cfg := config.NewConfig()
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Set an alias
	cfg, err := config.LoadFrom(cfgPath)
	require.NoError(t, err)

	err = cfg.SetAlias("wf", "get workflows", isBuiltinCommand)
	require.NoError(t, err)

	// Save it
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Verify it was saved
	cfg, err = config.LoadFrom(cfgPath)
	require.NoError(t, err)
	exp, ok := cfg.GetAlias("wf")
	require.True(t, ok)
	require.Equal(t, "get workflows", exp)

	// List aliases
	entries := cfg.ListAliases()
	require.Len(t, entries, 1)
	require.Equal(t, "wf", entries[0].Name)
	require.Equal(t, "get workflows", entries[0].Expansion)
}

func TestAliasDelete(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config")

	// Create config with an alias
	cfg := config.NewConfig()
	require.NoError(t, cfg.SetAlias("wf", "get workflows", nil))
	require.NoError(t, cfg.SetAlias("prod-wf", "get workflows --context=production", nil))
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Delete one alias
	cfg, err := config.LoadFrom(cfgPath)
	require.NoError(t, err)

	err = cfg.DeleteAlias("wf")
	require.NoError(t, err)

	// Save it
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Verify it was deleted
	cfg, err = config.LoadFrom(cfgPath)
	require.NoError(t, err)
	_, ok := cfg.GetAlias("wf")
	require.False(t, ok)

	// Verify other alias still exists
	exp, ok := cfg.GetAlias("prod-wf")
	require.True(t, ok)
	require.Equal(t, "get workflows --context=production", exp)
}

func TestAliasDeleteMultiple(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config")

	// Create config with multiple aliases
	cfg := config.NewConfig()
	require.NoError(t, cfg.SetAlias("wf", "get workflows", nil))
	require.NoError(t, cfg.SetAlias("prod-wf", "get workflows --context=production", nil))
	require.NoError(t, cfg.SetAlias("deploy", "apply -f $1", nil))
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Delete multiple aliases
	cfg, err := config.LoadFrom(cfgPath)
	require.NoError(t, err)

	err = cfg.DeleteAlias("wf")
	require.NoError(t, err)
	err = cfg.DeleteAlias("deploy")
	require.NoError(t, err)

	// Save it
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Verify they were deleted
	cfg, err = config.LoadFrom(cfgPath)
	require.NoError(t, err)
	_, ok := cfg.GetAlias("wf")
	require.False(t, ok)
	_, ok = cfg.GetAlias("deploy")
	require.False(t, ok)

	// Verify other alias still exists
	exp, ok := cfg.GetAlias("prod-wf")
	require.True(t, ok)
	require.Equal(t, "get workflows --context=production", exp)
}

func TestAliasExportImport(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config")
	exportPath := filepath.Join(tmpDir, "aliases.yaml")

	// Create config with aliases
	cfg := config.NewConfig()
	require.NoError(t, cfg.SetAlias("wf", "get workflows", nil))
	require.NoError(t, cfg.SetAlias("prod-wf", "get workflows --context=production", nil))
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Export aliases
	cfg, err := config.LoadFrom(cfgPath)
	require.NoError(t, err)

	err = cfg.ExportAliases(exportPath)
	require.NoError(t, err)

	// Verify export file exists
	_, err = os.Stat(exportPath)
	require.NoError(t, err)

	// Create a new config
	cfg2Path := filepath.Join(tmpDir, "config2")
	cfg2 := config.NewConfig()
	require.NoError(t, cfg2.SaveTo(cfg2Path))

	// Import into new config
	cfg2, err = config.LoadFrom(cfg2Path)
	require.NoError(t, err)

	conflicts, err := cfg2.ImportAliases(exportPath, false, nil)
	require.NoError(t, err)
	require.Empty(t, conflicts)

	// Save it
	require.NoError(t, cfg2.SaveTo(cfg2Path))

	// Verify imported
	cfg2, err = config.LoadFrom(cfg2Path)
	require.NoError(t, err)

	exp, ok := cfg2.GetAlias("wf")
	require.True(t, ok)
	require.Equal(t, "get workflows", exp)

	exp, ok = cfg2.GetAlias("prod-wf")
	require.True(t, ok)
	require.Equal(t, "get workflows --context=production", exp)
}

func TestAliasCannotShadowBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config")

	// Create initial config
	cfg := config.NewConfig()
	require.NoError(t, cfg.SaveTo(cfgPath))

	// Try to set an alias that shadows "get"
	cfg, err := config.LoadFrom(cfgPath)
	require.NoError(t, err)

	err = cfg.SetAlias("get", "describe workflows", isBuiltinCommand)
	require.ErrorContains(t, err, "built-in command")

	// Verify it was not saved
	_, ok := cfg.GetAlias("get")
	require.False(t, ok)
}
