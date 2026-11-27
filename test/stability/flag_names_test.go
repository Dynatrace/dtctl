package stability_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/cmd"
)

// kebabFlagName is the one spelling every flag name has: lower-case words
// joined by single hyphens, starting with a letter.
var kebabFlagName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// flagNameExemptions lists flags (by "command path --flag") that predate the
// rule and are not kebab-case. Each needs a reason; the list can only shrink.
var flagNameExemptions = map[string]string{}

// TestEveryFlagNameIsKebabCase holds every flag name in the complete command
// tree -- development commands and serve included -- to kebab-case. The aws,
// azure and gcp commands used camelCase and spelling aliases until the rename
// (#525); an alias is a flag of its own, so it is covered too.
func TestEveryFlagNameIsKebabCase(t *testing.T) {
	seen := map[string]bool{}
	var bad []string
	for _, key := range cmd.FlagNames() {
		name := key[strings.LastIndex(key, " --")+3:]
		if kebabFlagName.MatchString(name) {
			continue
		}
		if _, ok := flagNameExemptions[key]; ok {
			seen[key] = true
			continue
		}
		bad = append(bad, key)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("flag names must match %s:\n  %s", kebabFlagName, strings.Join(bad, "\n  "))
	}
	for key := range flagNameExemptions {
		if !seen[key] {
			t.Errorf("exemption %q no longer matches a flag; remove it", key)
		}
	}
}
