package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateHome points the user home directory at a fresh temp dir so global
// skill locations never resolve to the developer's real home.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// writeMarker creates <root>/dt-dql-essentials/SKILL.md.
func writeMarker(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, companionMarkerSkill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skillFileName), []byte("---\nname: dt-dql-essentials\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompanionInstalled_NotInstalled(t *testing.T) {
	isolateHome(t)
	baseDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, baseDir, false, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if CompanionInstalled(agent, baseDir, result.Path) {
		t.Error("CompanionInstalled = true with no dt-dql-essentials skill anywhere")
	}
}

func TestCompanionInstalled_NextToDtctl(t *testing.T) {
	isolateHome(t)
	baseDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, baseDir, false, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	writeMarker(t, filepath.Dir(result.Path))

	if !CompanionInstalled(agent, baseDir, result.Path) {
		t.Error("CompanionInstalled = false with dt-dql-essentials next to the dtctl skill")
	}
}

func TestCompanionInstalled_OtherScope(t *testing.T) {
	// dtctl installed project-local, Dynatrace for AI installed globally for the
	// same agent: the agent discovers both, so no hint is due.
	home := isolateHome(t)
	baseDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, baseDir, false, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	writeMarker(t, filepath.Dir(filepath.Join(home, agent.GlobalPath)))

	if !CompanionInstalled(agent, baseDir, result.Path) {
		t.Error("CompanionInstalled = false with dt-dql-essentials in the agent's global skills dir")
	}
}

func TestCompanionInstalled_Symlinked(t *testing.T) {
	// `npx skills add` keeps one copy under .agents/skills and symlinks it into
	// each agent's skills dir.
	isolateHome(t)
	baseDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, baseDir, false, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	shared := filepath.Join(baseDir, ".agents", "skills")
	writeMarker(t, shared)
	link := filepath.Join(filepath.Dir(result.Path), companionMarkerSkill)
	if err := os.Symlink(filepath.Join(shared, companionMarkerSkill), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if !CompanionInstalled(agent, baseDir, result.Path) {
		t.Error("CompanionInstalled = false with a symlinked dt-dql-essentials skill")
	}
}

func TestCompanionInstalled_OtherAgentDoesNotCount(t *testing.T) {
	// A Dynatrace for AI install for a different agent is invisible to this
	// one, so the hint is still due.
	isolateHome(t)
	baseDir := t.TempDir()
	claude, _ := FindAgent("claude")
	cursor, _ := FindAgent("cursor")

	result, err := Install(claude, baseDir, false, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	writeMarker(t, filepath.Dir(filepath.Join(baseDir, cursor.ProjectPath)))

	if CompanionInstalled(claude, baseDir, result.Path) {
		t.Error("CompanionInstalled = true from another agent's skills dir")
	}
}
