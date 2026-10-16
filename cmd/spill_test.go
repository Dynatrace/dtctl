package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
)

// newSpillTestCmd builds a command carrying the same spill flags as queryCmd so
// resolveSpillOptions can be exercised in isolation.
func newSpillTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "test"}
	c.Flags().String("spill", "", "")
	c.Flags().Lookup("spill").NoOptDefVal = "always"
	c.Flags().String("spill-to", "", "")
	c.Flags().String("spill-format", "", "")
	c.Flags().String("spill-threshold", "", "")
	return c
}

func emptyConfig() *config.Config {
	return &config.Config{}
}

func TestResolveSpillOptions_Defaults(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()

	gFlags.agentMode = false
	got, err := resolveSpillOptions(newSpillTestCmd(), emptyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != exec.SpillNever {
		t.Errorf("bare default mode = %q, want never", got.Mode)
	}

	gFlags.agentMode = true
	got, err = resolveSpillOptions(newSpillTestCmd(), emptyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != exec.SpillAuto {
		t.Errorf("agent default mode = %q, want auto", got.Mode)
	}
	if got.Threshold != exec.DefaultSpillThresholdBytes {
		t.Errorf("default threshold = %d, want %d", got.Threshold, exec.DefaultSpillThresholdBytes)
	}
}

func TestResolveSpillOptions_FlagBeatsEnvBeatsConfig(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = false

	cfg := &config.Config{Spill: config.SpillConfig{Mode: "auto"}}

	// config only
	got, _ := resolveSpillOptions(newSpillTestCmd(), cfg)
	if got.Mode != exec.SpillAuto {
		t.Errorf("config mode = %q, want auto", got.Mode)
	}

	// env beats config
	t.Setenv("DTCTL_SPILL", "always")
	got, _ = resolveSpillOptions(newSpillTestCmd(), cfg)
	if got.Mode != exec.SpillAlways {
		t.Errorf("env mode = %q, want always", got.Mode)
	}

	// flag beats env
	c := newSpillTestCmd()
	_ = c.Flags().Set("spill", "never")
	got, _ = resolveSpillOptions(c, cfg)
	if got.Mode != exec.SpillNever {
		t.Errorf("flag mode = %q, want never", got.Mode)
	}
}

func TestResolveSpillOptions_BareSpill(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = false

	c := newSpillTestCmd()
	// emulate bare --spill (NoOptDefVal)
	_ = c.Flags().Lookup("spill").Value.Set("always")
	c.Flags().Lookup("spill").Changed = true
	got, err := resolveSpillOptions(c, emptyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != exec.SpillAlways {
		t.Errorf("bare --spill mode = %q, want always", got.Mode)
	}
}

func TestResolveSpillOptions_Threshold(t *testing.T) {
	c := newSpillTestCmd()
	_ = c.Flags().Set("spill-threshold", "10KB")
	got, err := resolveSpillOptions(c, emptyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Threshold != 10*1024 {
		t.Errorf("threshold = %d, want %d", got.Threshold, 10*1024)
	}

	c = newSpillTestCmd()
	_ = c.Flags().Set("spill-threshold", "bogus")
	if _, err := resolveSpillOptions(c, emptyConfig()); err == nil {
		t.Error("expected error for invalid threshold")
	}
}

func TestResolveSpillOptions_SpillToImpliesAlways(t *testing.T) {
	c := newSpillTestCmd()
	_ = c.Flags().Set("spill-to", "/tmp/out.csv")
	got, err := resolveSpillOptions(c, emptyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != exec.SpillAlways {
		t.Errorf("--spill-to mode = %q, want always", got.Mode)
	}
	if got.ToPath != "/tmp/out.csv" {
		t.Errorf("ToPath = %q", got.ToPath)
	}
}

func TestResolveSpillOptions_Conflicts(t *testing.T) {
	// --spill-to + --spill=never -> error
	c := newSpillTestCmd()
	_ = c.Flags().Set("spill-to", "/tmp/out.json")
	_ = c.Flags().Set("spill", "never")
	if _, err := resolveSpillOptions(c, emptyConfig()); err == nil {
		t.Error("expected error for --spill-to + --spill=never")
	}

	// --spill-format disagrees with --spill-to extension -> error
	c = newSpillTestCmd()
	_ = c.Flags().Set("spill-to", "/tmp/out.csv")
	_ = c.Flags().Set("spill-format", "json")
	if _, err := resolveSpillOptions(c, emptyConfig()); err == nil {
		t.Error("expected error for --spill-format vs --spill-to extension disagreement")
	}

	// agreeing format + extension -> ok
	c = newSpillTestCmd()
	_ = c.Flags().Set("spill-to", "/tmp/out.csv")
	_ = c.Flags().Set("spill-format", "csv")
	if _, err := resolveSpillOptions(c, emptyConfig()); err != nil {
		t.Errorf("agreeing format/extension should be ok: %v", err)
	}
}

func TestResolveSpillOptions_InvalidMode(t *testing.T) {
	c := newSpillTestCmd()
	_ = c.Flags().Set("spill", "bogus")
	if _, err := resolveSpillOptions(c, emptyConfig()); err == nil {
		t.Error("expected error for invalid --spill value")
	}
}

func TestSpillWritesParquet(t *testing.T) {
	cases := []struct {
		name string
		opts exec.SpillOptions
		want bool
	}{
		{"default jsonl", exec.SpillOptions{}, false},
		{"format parquet", exec.SpillOptions{Format: "parquet"}, true},
		{"format PARQUET cased", exec.SpillOptions{Format: "PARQUET"}, true},
		{"format jsonl", exec.SpillOptions{Format: "jsonl"}, false},
		{"to .parquet", exec.SpillOptions{ToPath: "/tmp/out.parquet"}, true},
		{"to .jsonl beats parquet format", exec.SpillOptions{ToPath: "/tmp/out.jsonl", Format: "parquet"}, false},
		{"extensionless to falls back to format", exec.SpillOptions{ToPath: "/tmp/out", Format: "parquet"}, true},
	}
	for _, c := range cases {
		if got := spillWritesParquet(c.opts); got != c.want {
			t.Errorf("%s: spillWritesParquet = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestResolveSpillOptions_WithoutHostDiskCapability: spilling writes a file to
// the host disk and returns a path to read it back, so an embedded invocation
// (which grants no capabilities) must not spill. The automatic case degrades
// silently to "never" — the caller gets its rows inline, which is what it can
// actually use — while an explicit request fails, because silently inlining a
// result the command line asked to spill would misreport what happened.
func TestResolveSpillOptions_WithoutHostDiskCapability(t *testing.T) {
	origAgent := agentMode(context.Background())
	origCaps := SetCapabilities(Capabilities{}) // grant nothing, as embedders do
	defer func() {
		gFlags.agentMode = origAgent
		SetCapabilities(origCaps)
	}()

	// Agent mode would default to auto; without the capability it is never.
	gFlags.agentMode = true
	got, err := resolveSpillOptions(newSpillTestCmd(), emptyConfig())
	if err != nil {
		t.Fatalf("automatic spill must degrade quietly, got error: %v", err)
	}
	if got.Mode != exec.SpillNever {
		t.Errorf("mode = %q, want never", got.Mode)
	}
	if got.ToPath != "" || got.Dir != "" {
		t.Errorf("no host path may be resolved: ToPath=%q Dir=%q", got.ToPath, got.Dir)
	}

	// An explicit --spill-to is a capability error, not a silent no-op.
	c := newSpillTestCmd()
	if err := c.Flags().Set("spill-to", "/tmp/tenant-chosen.jsonl"); err != nil {
		t.Fatal(err)
	}
	_, err = resolveSpillOptions(c, emptyConfig())
	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("--spill-to without the capability = %v, want *CapabilityError", err)
	}

	// So is an explicit --spill=always.
	c = newSpillTestCmd()
	if err := c.Flags().Set("spill", "always"); err != nil {
		t.Fatal(err)
	}
	_, err = resolveSpillOptions(c, emptyConfig())
	if !errors.As(err, &capErr) {
		t.Fatalf("--spill=always without the capability = %v, want *CapabilityError", err)
	}

	// An explicit --spill=never asks for nothing, so it is not an error.
	c = newSpillTestCmd()
	if err := c.Flags().Set("spill", "never"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSpillOptions(c, emptyConfig()); err != nil {
		t.Fatalf("--spill=never without the capability = %v, want no error", err)
	}
}

// TestResolveSpillOptions_CLIKeepsSpilling guards the other direction: the CLI
// grants HostDiskSpill, so none of the above changes local behaviour.
func TestResolveSpillOptions_CLIKeepsSpilling(t *testing.T) {
	origAgent := agentMode(context.Background())
	origCaps := SetCapabilities(AllCapabilities())
	defer func() {
		gFlags.agentMode = origAgent
		SetCapabilities(origCaps)
	}()

	gFlags.agentMode = true
	got, err := resolveSpillOptions(newSpillTestCmd(), emptyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != exec.SpillAuto {
		t.Errorf("CLI agent-mode spill = %q, want auto", got.Mode)
	}
}
