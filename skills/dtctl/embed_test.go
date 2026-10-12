package dtctlskill

import (
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Every reference SKILL.md links to must ship: embedded here, and a file git
// will commit. A reference the repository ignores is embedded in a local build
// and missing from every clean checkout and release.
func TestSkillReferencesAreCommitted(t *testing.T) {
	skill, err := fs.ReadFile(Content, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\]\((references/[^)#]+\.md)\)`).FindAllStringSubmatch(string(skill), -1)
	if len(links) == 0 {
		t.Fatal("SKILL.md links no reference; the pattern no longer matches its links")
	}

	out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "--", ".").Output()
	if err != nil {
		t.Skipf("not in a git checkout: %v", err)
	}
	committed := map[string]bool{}
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		committed[f] = true
	}
	for _, link := range links {
		ref := link[1]
		if _, err := fs.Stat(Content, ref); err != nil {
			t.Errorf("SKILL.md links %s, which is not embedded: %v", ref, err)
		}
		if !committed[ref] {
			t.Errorf("SKILL.md links %s, which git ignores", ref)
		}
	}
}
