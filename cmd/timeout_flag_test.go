package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

	// Zero stays valid: `wait query` reads it as unlimited.
	if err := newFS(time.Minute).Parse([]string{"--timeout=0"}); err != nil {
		t.Errorf("0: %v", err)
	}
	for _, neg := range []string{"-5s", "-1m"} {
		err := newFS(time.Minute).Parse([]string{"--timeout=" + neg})
		want := `invalid --timeout "` + neg + `": must not be negative; use a duration such as 60s`
		if err == nil || enhanceFlagError(rootCmd, err).Error() != want {
			t.Errorf("%q: error = %v, want %q", neg, err, want)
		}
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
