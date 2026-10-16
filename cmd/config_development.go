package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// setDevelopmentKey handles `dtctl config set development.<feature> on|off`.
//
// An unknown feature key is rejected. The whole point of the development tier
// is that the feature does not exist until it is enabled, so a typo that
// silently wrote a dead key would leave the caller waiting for a command that
// is never going to appear.
func setDevelopmentKey(cfg *config.Config, key, value string) error {
	feature := strings.TrimPrefix(key, "development.")
	if feature == "" {
		return fmt.Errorf("configuration key %q names no feature; write e.g. development.serve", key)
	}
	on, err := parseOnOff(value)
	if err != nil {
		return fmt.Errorf("development.%s: %w", feature, err)
	}
	known := stability.DefaultRegistry().Features()
	if feature != config.DevelopmentAll && !contains(known, feature) {
		if len(known) == 0 {
			return fmt.Errorf("unknown development feature %q; this build has none", feature)
		}
		return fmt.Errorf("unknown development feature %q; this build has: %s",
			feature, strings.Join(known, ", "))
	}
	cfg.SetDevelopmentFeature(feature, on)
	return nil
}

// parseOnOff reads the truthiness vocabulary `config set` accepts for a
// boolean. Deliberately narrow: an unrecognized value is an error rather than
// a silent false, because a silent false on an opt-in reads as "the feature is
// broken".
func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1", "yes", "enabled":
		return true, nil
	case "off", "false", "0", "no", "disabled":
		return false, nil
	}
	if b, err := strconv.ParseBool(value); err == nil {
		return b, nil
	}
	return false, fmt.Errorf("value %q is not on or off", value)
}

// contains reports whether a sorted-or-not string slice holds v.
func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// configListDevelopmentCmd lists the development-tier features this build
// carries and whether each is enabled.
//
// It is the discovery surface for a tier that is deliberately absent from help
// and from `dtctl commands`: the features do not exist until opted into, so
// something has to be able to name them, and a command a caller must ask for
// by name is a much narrower disclosure than a badge in `--help`.
var configListDevelopmentCmd = newConfigListDevelopmentCmd()

func newConfigListDevelopmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "list-development",
		Aliases: []string{"list-dev"},
		Short:   "List development-tier features and whether they are enabled",
		Long: `List the development-tier features this dtctl build carries.

Development features are unfinished. They carry no stability guarantees, may
change or be removed without notice, and are not registered on the command tree
at all until enabled — an invocation of a disabled one is an unknown command.

Enable one persistently:
  dtctl config set development.<feature> on

Enable one for a single process:
  ` + config.DevelopmentEnvVar + `=<feature> dtctl <command>
  ` + config.DevelopmentEnvVar + `=` + config.DevelopmentAll + ` dtctl <command>   # every feature
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			features := stability.DefaultRegistry().Features()
			// Resolved from the config this invocation actually loaded (honoring
			// --config / DTCTL_CONFIG), not from the ambient default: a listing
			// that reported a different file's opt-ins than the one being edited
			// would be worse than no listing.
			enabled := map[string]bool{}
			if cfg, err := loadConfig(cmdContext(cmd)); err == nil {
				enabled = legacyDevelopmentFeatures(cmdContext(cmd), cfg.EnabledDevelopmentFeatures())
			}

			switch outputFormat(cmdContext(cmd)) {
			case "json":
				enc := json.NewEncoder(currentStdout(cmdContext(cmd)))
				enc.SetIndent("", "  ")
				return enc.Encode(developmentRows(features, enabled))
			case "yaml", "yml":
				enc := yaml.NewEncoder(currentStdout(cmdContext(cmd)))
				enc.SetIndent(2)
				if err := enc.Encode(developmentRows(features, enabled)); err != nil {
					return err
				}
				return enc.Close()
			}

			if len(features) == 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "This build carries no development-tier features.")
				return nil
			}
			w := tabwriter.NewWriter(currentStdout(cmdContext(cmd)), 0, 8, 2, ' ', 0)
			fmt.Fprintln(w, "FEATURE\tCOMMAND\tENABLED")
			for _, f := range features {
				path, _ := stability.DefaultRegistry().Path(f)
				state := "no"
				if stability.Enabled(f, enabled) {
					state = "yes"
				}
				fmt.Fprintf(w, "%s\tdtctl %s\t%s\n", f, path, state)
			}
			return w.Flush()
		},
	}
	stability.MarkStable(c)
	return c
}

// developmentRow is one machine-readable development-feature entry.
type developmentRow struct {
	Feature string `json:"feature" yaml:"feature"`
	Command string `json:"command" yaml:"command"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

// developmentRows builds the machine-readable listing, in the same order as the
// table.
func developmentRows(features []string, enabled map[string]bool) []developmentRow {
	rows := make([]developmentRow, 0, len(features))
	for _, f := range features {
		path, _ := stability.DefaultRegistry().Path(f)
		rows = append(rows, developmentRow{
			Feature: f,
			Command: "dtctl " + path,
			Enabled: stability.Enabled(f, enabled),
		})
	}
	return rows
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
