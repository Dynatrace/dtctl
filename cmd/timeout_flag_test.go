package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// Every flag named "timeout" in the command tree is a duration.
func TestTimeoutFlagsAreDurations(t *testing.T) {
	found := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			if f := fs.Lookup("timeout"); f != nil {
				found++
				if f.Value.Type() != "duration" {
					t.Errorf("%s: --timeout has type %q, want duration", c.CommandPath(), f.Value.Type())
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	walk(accountCmd) // development-only, so not always attached to rootCmd
	if found < 6 {
		t.Errorf("found only %d --timeout flags; the walk is missing commands", found)
	}
}

func TestTimeoutFlagParsing(t *testing.T) {
	newFS := func(def time.Duration) *pflag.FlagSet {
		fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
		timeoutFlagVar(fs, def, "u")
		return fs
	}
	fs := newFS(5 * time.Minute)
	if got, _ := fs.GetDuration("timeout"); got != 5*time.Minute {
		t.Errorf("default = %v", got)
	}
	if err := fs.Parse([]string{"--timeout", "90s"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.GetDuration("timeout"); got != 90*time.Second {
		t.Errorf("parsed = %v", got)
	}

	for _, bad := range []string{"60", "abc", ""} {
		err := newFS(time.Minute).Parse([]string{"--timeout=" + bad})
		if err == nil {
			t.Errorf("%q: want error", bad)
			continue
		}
		want := `invalid --timeout "` + bad + `": use a duration such as 60s`
		if got := enhanceFlagError(rootCmd, err).Error(); got != want {
			t.Errorf("%q: error = %q, want %q", bad, got, want)
		}
	}
}

// The flags that used to be integers reject unit-less numbers, zero and negatives; the
// others keep their previous parsing (wait query: 0 and negatives = unlimited).
func TestStrictTimeoutFlag(t *testing.T) {
	strictFS := func() *pflag.FlagSet {
		fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
		strictTimeoutFlagVar(fs, time.Minute, "u")
		return fs
	}
	for _, v := range []string{"0", "+0", "-0", "1.5", "-5s"} {
		err := strictFS().Parse([]string{"--timeout=" + v})
		if err == nil || !strings.Contains(enhanceFlagError(rootCmd, err).Error(), "invalid --timeout") {
			t.Errorf("%q: error = %v", v, err)
		}
	}
	if err := strictFS().Parse([]string{"--timeout=0"}); err == nil || !strings.Contains(err.Error(), `use a duration such as 60s`) {
		t.Errorf("bare 0: %v", err)
	}
	for _, v := range []string{"0s", "0m", "-0s", "-1s"} {
		err := strictFS().Parse([]string{"--timeout=" + v})
		want := `invalid --timeout "` + v + `": must be greater than zero; use a duration such as 60s`
		if err == nil || enhanceFlagError(rootCmd, err).Error() != want {
			t.Errorf("%q: error = %v, want %q", v, err, want)
		}
	}
	if err := strictFS().Parse([]string{"--timeout=1ms"}); err != nil {
		t.Errorf("1ms: %v", err)
	}
	for _, path := range []string{"wait query"} {
		c, _, err := rootCmd.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"0", "-1s"} {
			if err := c.Flags().Set("timeout", v); err != nil {
				t.Errorf("%s --timeout %s must stay valid: %v", path, v, err)
			}
		}
		_ = c.Flags().Set("timeout", c.Flags().Lookup("timeout").DefValue)
	}
	for _, path := range [][]string{{"exec", "analyzer"}, {"exec", "slo"}} {
		c, _, _ := rootCmd.Find(path)
		for _, v := range []string{"0", "0s", "-1s"} {
			if err := c.Flags().Set("timeout", v); err == nil {
				t.Errorf("%v --timeout %s must be rejected", path, v)
			}
		}
	}
}

// A bad --timeout is invalid input: usage exit code, validation_error envelope,
// also when --agent follows the bad value and pflag never reaches it.
func TestTimeoutErrorIsUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "slo", "x", "--timeout", "60", "--agent"},
		{"exec", "slo", "x", "--agent", "--timeout", "60"},
		{"exec", "analyzer", "a", "--timeout=0", "-A"},
	} {
		isolatedConfig(t, "contexts: []\n")
		code, out := captureRun(t, args, RunOptions{})
		if code != client.ExitUsageError {
			t.Errorf("%v: exit = %d, want %d", args, code, client.ExitUsageError)
		}
		env := parseErrorEnvelope(t, out)
		if env.Error.Code != "validation_error" || !strings.Contains(env.Error.Message, "invalid --timeout") {
			t.Errorf("%v: envelope = %+v", args, env.Error)
		}
	}
}

func TestTimeoutBareIntegerRejectedOnCommands(t *testing.T) {
	for _, path := range [][]string{{"exec", "analyzer"}, {"exec", "slo"}, {"auth", "login"}, {"account", "login"}} {
		root := rootCmd
		if path[0] == "account" {
			root = accountCmd
			path = path[1:]
		}
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Flags().Lookup("timeout") == nil {
			t.Fatalf("%v: command or --timeout flag not found (%v)", path, err)
		}
		err = c.Flags().Set("timeout", "60")
		if err == nil || !strings.Contains(err.Error(), `use a duration such as 60s`) {
			t.Errorf("%v: Set(60) error = %v", path, err)
		}
		if err := c.Flags().Set("timeout", "60s"); err != nil {
			t.Errorf("%v: Set(60s): %v", path, err)
		}
		_ = c.Flags().Set("timeout", c.Flags().Lookup("timeout").DefValue)
	}
}

// wait query / exec workflow keep their accepted values, but a bad duration
// gets the same migration hint, usage exit code and envelope.
func TestPlainDurationTimeoutGetsHint(t *testing.T) {
	for _, args := range [][]string{
		{"wait", "query", "fetch logs", "--timeout", "60", "--agent"},
		{"exec", "workflow", "wf", "--wait", "--timeout=60", "-A"},
	} {
		isolatedConfig(t, "contexts: []\n")
		code, out := captureRun(t, args, RunOptions{})
		if code != client.ExitUsageError {
			t.Errorf("%v: exit = %d", args, code)
		}
		env := parseErrorEnvelope(t, out)
		want := `invalid --timeout "60": use a duration such as 60s`
		if env.Error.Code != "validation_error" || env.Error.Message != want {
			t.Errorf("%v: envelope = %+v", args, env.Error)
		}
	}
}

// --agent=false after a bad value is an explicit opt-out: human error text.
func TestTimeoutErrorRespectsAgentOptOut(t *testing.T) {
	for _, flag := range []string{"--agent=false", "--plain=false"} {
		isolatedConfig(t, "contexts: []\n")
		_, out := captureRun(t, []string{"exec", "slo", "x", "--timeout", "60", flag}, RunOptions{})
		if strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Errorf("%s: got a structured envelope: %s", flag, out)
		}
	}
	if !rawBoolFlagEnabled([]string{"--agent"}, "--agent") || !rawBoolFlagEnabled([]string{"--agent=true"}, "--agent") ||
		rawBoolFlagEnabled([]string{"--agent=false"}, "--agent") || rawBoolFlagEnabled(nil, "--agent") {
		t.Error("rawBoolFlagEnabled misparses")
	}
	// A positional after the "--" terminator is not a flag.
	if !rawBoolFlagEnabled([]string{"--agent", "--", "--agent=false"}, "--agent") ||
		rawBoolFlagEnabled([]string{"--", "--agent"}, "--agent") {
		t.Error("rawBoolFlagEnabled scans past --")
	}
}
