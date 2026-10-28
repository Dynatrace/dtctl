package commands

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/version"
)

// GenerateHowto writes an LLM-optimized markdown reference guide to w.
// The content is derived from the given Listing to stay in sync with the
// actual command tree.
func GenerateHowto(w io.Writer, l *Listing) error {
	var b strings.Builder

	b.WriteString("# dtctl Quick Reference\n\n")
	b.WriteString(fmt.Sprintf("Version: %s\n\n", version.Version))

	// Command model
	b.WriteString("## Command Model\n\n")
	b.WriteString("dtctl uses a verb-noun pattern: `dtctl <verb> <resource> [flags]`\n\n")

	// Available verbs
	b.WriteString("## Verbs\n\n")
	verbNames := sortedKeys(l.Verbs)
	for _, name := range verbNames {
		verb := l.Verbs[name]
		mutLabel := ""
		if verb.Mutating {
			mutLabel = " (mutating)"
		}
		b.WriteString(fmt.Sprintf("- **%s** — %s%s\n", name, verb.Description, mutLabel))

		if len(verb.Resources) > 0 {
			b.WriteString(fmt.Sprintf("  Resources: %s\n", strings.Join(verb.Resources, ", ")))
		}
	}
	b.WriteString("\n")

	// Common workflows
	b.WriteString("## Common Workflows\n\n")

	b.WriteString("### Deploying a workflow\n\n")
	b.WriteString("1. `dtctl apply -f workflow.yaml --dry-run --show-diff` — preview\n")
	b.WriteString("2. `dtctl apply -f workflow.yaml` — apply\n")
	b.WriteString("3. `dtctl describe workflow <id>` — verify\n\n")

	b.WriteString("### Querying data\n\n")
	b.WriteString("1. `dtctl query --query \"fetch logs | filter status == 'ERROR' | limit 10\"`\n")
	b.WriteString("2. `dtctl query --query \"...\" -o chart` — visualize\n\n")

	b.WriteString("### CI/CD pipeline\n\n")
	b.WriteString("1. `dtctl apply -f resource.yaml --plain` — deploy\n")
	b.WriteString("2. `dtctl wait query --for \"count > 0\" --timeout 5m --query \"...\"` — wait for condition\n")
	b.WriteString("3. `dtctl verify query --query \"...\"` — validate DQL syntax (exit code 0/1)\n\n")

	// Safety levels
	b.WriteString("## Safety Levels\n\n")
	b.WriteString("dtctl contexts have safety levels that restrict mutating commands:\n\n")
	b.WriteString("- `readonly` — blocks all create/update/delete\n")
	b.WriteString("- `readwrite-mine` — allows modifying own resources only\n")
	b.WriteString("- `readwrite-all` — allows modifying all resources (default)\n")
	b.WriteString("- `dangerously-unrestricted` — allows all operations including bucket deletion\n\n")

	// Resource aliases
	b.WriteString("## Resource Aliases\n\n")
	if len(l.Aliases) > 0 {
		aliasKeys := sortedKeys(l.Aliases)
		for _, alias := range aliasKeys {
			b.WriteString(fmt.Sprintf("- `%s` → %s\n", alias, l.Aliases[alias]))
		}
		b.WriteString("\n")
	}

	// Time formats
	b.WriteString("## Time Formats\n\n")
	if l.TimeFormats != nil {
		b.WriteString(fmt.Sprintf("- Relative: %s\n", strings.Join(l.TimeFormats.Relative, ", ")))
		b.WriteString(fmt.Sprintf("- Absolute: %s\n", l.TimeFormats.Absolute))
		b.WriteString(fmt.Sprintf("- Unix: %s\n\n", l.TimeFormats.Unix))
	}

	// Output formats
	b.WriteString("## Output Formats\n\n")
	b.WriteString("table, wide, json, yaml, csv, chart, sparkline, barchart, braille\n\n")

	// Patterns
	if len(l.Patterns) > 0 {
		b.WriteString("## Recommended Patterns\n\n")
		for _, p := range l.Patterns {
			b.WriteString(fmt.Sprintf("- %s\n", p))
		}
		b.WriteString("\n")
	}

	// Antipatterns
	if len(l.Antipatterns) > 0 {
		b.WriteString("## Antipatterns\n\n")
		for _, p := range l.Antipatterns {
			b.WriteString(fmt.Sprintf("- %s\n", p))
		}
		b.WriteString("\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// sortedKeys returns the keys of a map sorted alphabetically.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
