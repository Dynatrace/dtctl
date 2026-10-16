package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

func newOutputBoundsTestCmd(args ...string) *cobra.Command {
	c := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	addOutputBoundFlags(c)
	c.SetArgs(args)
	_ = c.ParseFlags(args)
	return c
}

func TestResolveOutputBounds_Defaults(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()

	gFlags.agentMode = true
	got, err := resolveOutputBounds(newOutputBoundsTestCmd())
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxFieldChars != output.DefaultAgentMaxFieldChars || got.MaxOutputBytes != 0 {
		t.Errorf("agent defaults = %+v, want field cap %d and no budget", got, output.DefaultAgentMaxFieldChars)
	}

	gFlags.agentMode = false
	got, err = resolveOutputBounds(newOutputBoundsTestCmd())
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxFieldChars != 0 || got.MaxOutputBytes != 0 {
		t.Errorf("non-agent defaults = %+v, want no bounds (output unchanged)", got)
	}
}

func TestResolveOutputBounds_Flags(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = true

	cases := []struct {
		args      []string
		wantChars int
		wantBytes int64
	}{
		{[]string{"--max-field-chars", "0"}, 0, 0},
		{[]string{"--max-field-chars", "80"}, 80, 0},
		{[]string{"--max-output-bytes", "16KB"}, output.DefaultAgentMaxFieldChars, 16 * 1024},
		{[]string{"--max-output-bytes", "2000"}, output.DefaultAgentMaxFieldChars, 2000},
		{[]string{"--max-output-tokens", "4000"}, output.DefaultAgentMaxFieldChars, 16000},
	}
	for _, tc := range cases {
		got, err := resolveOutputBounds(newOutputBoundsTestCmd(tc.args...))
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got.MaxFieldChars != tc.wantChars || got.MaxOutputBytes != tc.wantBytes {
			t.Errorf("%v: got %+v, want chars=%d bytes=%d", tc.args, got, tc.wantChars, tc.wantBytes)
		}
	}
}

func TestResolveOutputBounds_Invalid(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = true

	for _, args := range [][]string{
		{"--max-field-chars", "-1"},
		{"--max-output-tokens", "-5"},
		{"--max-output-bytes", "lots"},
		{"--max-output-bytes", "NaNKB"},
		{"--max-output-tokens", "4611686018427387904"}, // × 4 overflows int64
	} {
		if _, err := resolveOutputBounds(newOutputBoundsTestCmd(args...)); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

func TestResolveOutputBounds_BytesAndTokensAreExclusive(t *testing.T) {
	c := newOutputBoundsTestCmd()
	c.SetArgs([]string{"--max-output-bytes", "1KB", "--max-output-tokens", "100"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "max-output-bytes") {
		t.Errorf("want a mutual-exclusion error, got %v", err)
	}
}

func TestResolveOutputBounds_IgnoredOutsideAgentMode(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = false

	got, err := resolveOutputBounds(newOutputBoundsTestCmd("--max-field-chars", "80", "--max-output-tokens", "100"))
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxFieldChars != 0 || got.MaxOutputBytes != 0 {
		t.Errorf("bounds must not apply outside agent mode: %+v", got)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "agent mode") {
		t.Errorf("an explicit flag that does nothing must warn: %v", got.Warnings)
	}
}

func TestQueryCommandRegistersOutputBoundFlags(t *testing.T) {
	for _, name := range []string{"max-field-chars", "max-output-bytes", "max-output-tokens"} {
		if queryCmd.Flags().Lookup(name) == nil {
			t.Errorf("query is missing --%s", name)
		}
	}
}
