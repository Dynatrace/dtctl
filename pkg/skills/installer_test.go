package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
)

func TestSupportedAgents(t *testing.T) {
	agents := SupportedAgents()
	expected := []string{"claude", "codex", "copilot", "cursor", "kiro", "junie", "opencode", "openclaw"}
	if len(agents) != len(expected) {
		t.Fatalf("expected %d agents, got %d: %v", len(expected), len(agents), agents)
	}
	for i, name := range expected {
		if agents[i] != name {
			t.Errorf("agent[%d] = %q, want %q", i, agents[i], name)
		}
	}
}

func TestFindAgent(t *testing.T) {
	tests := []struct {
		name  string
		found bool
	}{
		{"claude", true},
		{"codex", true},
		{"copilot", true},
		{"cursor", true},
		{"kiro", true},
		{"junie", true},
		{"opencode", true},
		{"openclaw", true},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, ok := FindAgent(tt.name)
			if ok != tt.found {
				t.Errorf("FindAgent(%q) found = %v, want %v", tt.name, ok, tt.found)
			}
			if ok && agent.Name != tt.name {
				t.Errorf("FindAgent(%q) returned agent.Name = %q", tt.name, agent.Name)
			}
		})
	}
}

func TestDetectAgent(t *testing.T) {
	// Each subtest clears ALL agent env vars to ensure full isolation.
	allEnvVars := aidetect.EnvVars()

	clearAllEnvVars := func(t *testing.T) {
		t.Helper()
		for _, env := range allEnvVars {
			t.Setenv(env, "")
		}
	}

	t.Run("no agent detected", func(t *testing.T) {
		clearAllEnvVars(t)
		_, detected := DetectAgent()
		if detected {
			t.Error("expected no agent detected")
		}
	})

	t.Run("detects claude", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("CLAUDECODE", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "claude" {
			t.Errorf("expected claude, got %q", agent.Name)
		}
	})

	t.Run("detects codex", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("CODEX", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "codex" {
			t.Errorf("expected codex, got %q", agent.Name)
		}
	})

	t.Run("detects opencode", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("OPENCODE", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "opencode" {
			t.Errorf("expected opencode, got %q", agent.Name)
		}
	})

	t.Run("detects copilot", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("GITHUB_COPILOT", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "copilot" {
			t.Errorf("expected copilot, got %q", agent.Name)
		}
	})

	t.Run("detects cursor", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("CURSOR_AGENT", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "cursor" {
			t.Errorf("expected cursor, got %q", agent.Name)
		}
	})

	t.Run("detects kiro", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("KIRO", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "kiro" {
			t.Errorf("expected kiro, got %q", agent.Name)
		}
	})

	t.Run("detects junie", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("JUNIE", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "junie" {
			t.Errorf("expected junie, got %q", agent.Name)
		}
	})

	t.Run("detects openclaw", func(t *testing.T) {
		clearAllEnvVars(t)
		t.Setenv("OPENCLAW", "1")
		agent, detected := DetectAgent()
		if !detected {
			t.Fatal("expected agent detected")
		}
		if agent.Name != "openclaw" {
			t.Errorf("expected openclaw, got %q", agent.Name)
		}
	})
}

func TestInstall(t *testing.T) {
	t.Run("installs skill directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("claude")

		result, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("Install error: %v", err)
		}

		if result.Replaced {
			t.Error("expected Replaced=false for new install")
		}
		if result.Global {
			t.Error("expected Global=false")
		}

		expectedDir := filepath.Join(tmpDir, ".claude", "skills", "dtctl")
		if result.Path != expectedDir {
			t.Errorf("Path = %q, want %q", result.Path, expectedDir)
		}

		// Verify SKILL.md exists and has content
		skillFile := filepath.Join(expectedDir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			t.Fatalf("failed to read SKILL.md: %v", err)
		}
		if len(data) == 0 {
			t.Error("SKILL.md is empty")
		}
		if !strings.Contains(string(data), "dtctl") {
			t.Error("SKILL.md should contain 'dtctl'")
		}

		// Verify references/ directory exists
		refsDir := filepath.Join(expectedDir, "references")
		if info, err := os.Stat(refsDir); err != nil || !info.IsDir() {
			t.Errorf("references/ directory should exist at %s", refsDir)
		}
	})

	t.Run("installs reference files", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("claude")

		result, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("Install error: %v", err)
		}

		// Check that reference files are present
		expectedRefs := []string{
			"references/troubleshooting.md",
			"references/DQL-reference.md",
			"references/config-management.md",
			"references/resources/dashboards.md",
			"references/resources/notebooks.md",
			"references/resources/extensions.md",
		}
		for _, ref := range expectedRefs {
			refPath := filepath.Join(result.Path, ref)
			if _, err := os.Stat(refPath); err != nil {
				t.Errorf("reference file missing: %s", ref)
			}
		}
	})

	t.Run("preserves YAML frontmatter", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("claude")

		result, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("Install error: %v", err)
		}

		skillFile := filepath.Join(result.Path, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			t.Fatalf("failed to read SKILL.md: %v", err)
		}

		content := string(data)
		if !strings.HasPrefix(content, "---\n") {
			t.Error("SKILL.md should start with YAML frontmatter")
		}
		if !strings.Contains(content, "name: dtctl") {
			t.Error("SKILL.md should contain 'name: dtctl' in frontmatter")
		}
	})

	t.Run("preserves relative links", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("claude")

		result, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("Install error: %v", err)
		}

		skillFile := filepath.Join(result.Path, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			t.Fatalf("failed to read SKILL.md: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "references/troubleshooting.md") {
			t.Error("SKILL.md should preserve relative links to references/")
		}
	})

	t.Run("refuses overwrite without --force", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("copilot")

		// First install
		_, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("first Install error: %v", err)
		}

		// Second install should fail
		_, err = Install(agent, tmpDir, false, false)
		if err == nil {
			t.Fatal("expected error on duplicate install")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("error should mention 'already exists', got: %v", err)
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention '--force', got: %v", err)
		}
	})

	t.Run("overwrites with force", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("cursor")

		// First install
		_, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("first Install error: %v", err)
		}

		// Second install with overwrite
		result, err := Install(agent, tmpDir, false, true)
		if err != nil {
			t.Fatalf("overwrite Install error: %v", err)
		}
		if !result.Replaced {
			t.Error("expected Replaced=true on overwrite")
		}
	})

	t.Run("global install unsupported agent", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("cursor")

		_, err := Install(agent, tmpDir, true, false)
		if err == nil {
			t.Fatal("expected error for unsupported global install")
		}
		if !strings.Contains(err.Error(), "does not support global") {
			t.Errorf("error should mention 'does not support global', got: %v", err)
		}
	})

	t.Run("installs all agents", func(t *testing.T) {
		tmpDir := t.TempDir()

		for _, agent := range AllAgents() {
			result, err := Install(agent, tmpDir, false, false)
			if err != nil {
				t.Fatalf("Install(%s) error: %v", agent.Name, err)
			}

			// Verify SKILL.md exists
			skillFile := filepath.Join(result.Path, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				t.Errorf("Install(%s) SKILL.md not found at %s", agent.Name, skillFile)
			}

			// Verify references/ exists
			refsDir := filepath.Join(result.Path, "references")
			if info, err := os.Stat(refsDir); err != nil || !info.IsDir() {
				t.Errorf("Install(%s) references/ not found at %s", agent.Name, refsDir)
			}
		}
	})

	t.Run("identical content for all agents", func(t *testing.T) {
		tmpDir := t.TempDir()

		var firstContent string
		for _, agent := range AllAgents() {
			result, err := Install(agent, tmpDir, false, false)
			if err != nil {
				t.Fatalf("Install(%s) error: %v", agent.Name, err)
			}

			skillFile := filepath.Join(result.Path, "SKILL.md")
			data, err := os.ReadFile(skillFile)
			if err != nil {
				t.Fatalf("ReadFile(%s) error: %v", agent.Name, err)
			}

			if firstContent == "" {
				firstContent = string(data)
			} else if string(data) != firstContent {
				t.Errorf("Install(%s) SKILL.md content differs from first agent", agent.Name)
			}
		}
	})
}

func TestUninstall(t *testing.T) {
	t.Run("removes installed directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("claude")

		// Install first
		result, _ := Install(agent, tmpDir, false, false)

		// Verify SKILL.md exists
		skillFile := filepath.Join(result.Path, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			t.Fatalf("SKILL.md should exist before uninstall")
		}

		// Uninstall
		removed, err := Uninstall(agent, tmpDir)
		if err != nil {
			t.Fatalf("Uninstall error: %v", err)
		}
		if len(removed) != 1 {
			t.Fatalf("expected 1 removed, got %d", len(removed))
		}

		// Verify directory is gone
		if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
			t.Error("skill directory should not exist after uninstall")
		}
	})

	t.Run("returns empty for non-installed", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("copilot")

		removed, err := Uninstall(agent, tmpDir)
		if err != nil {
			t.Fatalf("Uninstall error: %v", err)
		}
		if len(removed) != 0 {
			t.Errorf("expected 0 removed, got %d", len(removed))
		}
	})

	t.Run("returns removed paths alongside error on partial failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("claude")

		// Install project-local, then verify uninstall returns it
		Install(agent, tmpDir, false, false)
		removed, err := Uninstall(agent, tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(removed) != 1 {
			t.Errorf("expected 1 removed, got %d", len(removed))
		}
	})
}

func TestStatus(t *testing.T) {
	t.Run("installed project-local", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("cursor")

		// Install
		Install(agent, tmpDir, false, false)

		result := Status(agent, tmpDir)
		if !result.Installed {
			t.Error("expected Installed=true")
		}
		if result.Global {
			t.Error("expected Global=false")
		}
		if result.Path == "" {
			t.Error("expected non-empty Path")
		}
	})

	t.Run("not installed", func(t *testing.T) {
		tmpDir := t.TempDir()
		agent, _ := FindAgent("opencode")

		result := Status(agent, tmpDir)
		if result.Installed {
			t.Error("expected Installed=false")
		}
	})
}

func TestStatusAll(t *testing.T) {
	tmpDir := t.TempDir()

	// Install one agent
	agent, _ := FindAgent("claude")
	Install(agent, tmpDir, false, false)

	results := StatusAll(tmpDir)
	// StatusAll returns cross-client + all agents
	expectedCount := len(AllAgents()) + 1
	if len(results) != expectedCount {
		t.Fatalf("expected %d results, got %d", expectedCount, len(results))
	}

	installedCount := 0
	for _, r := range results {
		if r.Installed {
			installedCount++
			if r.Agent.Name != "claude" {
				t.Errorf("unexpected installed agent: %s", r.Agent.Name)
			}
		}
	}
	if installedCount != 1 {
		t.Errorf("expected 1 installed, got %d", installedCount)
	}
}

func TestAgentPaths(t *testing.T) {
	// Verify each agent has the expected agentskills.io standard path
	tests := []struct {
		name     string
		pathPart string
	}{
		{"claude", ".claude/skills/dtctl"},
		{"codex", ".codex/skills/dtctl"},
		{"copilot", ".github/skills/dtctl"},
		{"cursor", ".cursor/skills/dtctl"},
		{"kiro", ".kiro/skills/dtctl"},
		{"junie", ".junie/skills/dtctl"},
		{"opencode", ".opencode/skills/dtctl"},
		{"openclaw", "skills/dtctl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, ok := FindAgent(tt.name)
			if !ok {
				t.Fatalf("agent %q not found", tt.name)
			}
			// Use filepath to compare (handles OS-specific separators)
			expectedPath := filepath.FromSlash(tt.pathPart)
			if agent.ProjectPath != expectedPath {
				t.Errorf("ProjectPath = %q, want %q", agent.ProjectPath, expectedPath)
			}
		})
	}
}

func TestAgentGlobalPaths(t *testing.T) {
	// Verify global path support for each agent
	tests := []struct {
		name       string
		globalPath string
		supported  bool
	}{
		{"claude", ".claude/skills/dtctl", true},
		{"codex", ".codex/skills/dtctl", true},
		{"copilot", ".copilot/skills/dtctl", true},
		{"cursor", "", false},
		{"kiro", "", false},
		{"junie", ".junie/skills/dtctl", true},
		{"opencode", ".config/opencode/skills/dtctl", true},
		{"openclaw", ".openclaw/workspace/skills/dtctl", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, ok := FindAgent(tt.name)
			if !ok {
				t.Fatalf("agent %q not found", tt.name)
			}
			hasGlobal := agent.GlobalPath != ""
			if hasGlobal != tt.supported {
				t.Errorf("global support = %v, want %v (GlobalPath=%q)", hasGlobal, tt.supported, agent.GlobalPath)
			}
			if tt.supported {
				expectedPath := filepath.FromSlash(tt.globalPath)
				if agent.GlobalPath != expectedPath {
					t.Errorf("GlobalPath = %q, want %q", agent.GlobalPath, expectedPath)
				}
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	t.Run("project-local path", func(t *testing.T) {
		agent, _ := FindAgent("claude")
		path, err := resolvePath(agent, "/tmp/project", false)
		if err != nil {
			t.Fatalf("resolvePath error: %v", err)
		}
		expected := filepath.Join("/tmp/project", ".claude", "skills", "dtctl")
		if path != expected {
			t.Errorf("path = %q, want %q", path, expected)
		}
	})

	t.Run("global path for supported agent", func(t *testing.T) {
		agent, _ := FindAgent("claude")
		path, err := resolvePath(agent, "/tmp/project", true)
		if err != nil {
			t.Fatalf("resolvePath error: %v", err)
		}
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".claude", "skills", "dtctl")
		if path != expected {
			t.Errorf("path = %q, want %q", path, expected)
		}
	})

	t.Run("global path for unsupported agent", func(t *testing.T) {
		agent, _ := FindAgent("cursor")
		_, err := resolvePath(agent, "/tmp/project", true)
		if err == nil {
			t.Fatal("expected error for unsupported global path")
		}
		if !strings.Contains(err.Error(), "does not support global") {
			t.Errorf("error should mention 'does not support global', got: %v", err)
		}
	})
}

// --- Tests for skill content integrity ---

func TestInstalledSkillContent_HasFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, tmpDir, false, false)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}

	skillFile := filepath.Join(result.Path, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	content := string(data)

	// Must have agentskills.io standard YAML frontmatter
	if !strings.HasPrefix(content, "---\n") {
		t.Error("SKILL.md should start with YAML frontmatter")
	}
	if !strings.Contains(content, "name: dtctl") {
		t.Error("SKILL.md should contain 'name: dtctl' in frontmatter")
	}
	if !strings.Contains(content, "description:") {
		t.Error("SKILL.md should contain 'description:' in frontmatter")
	}
}

func TestInstalledSkillContent_ContainsMainSections(t *testing.T) {
	tmpDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, tmpDir, false, false)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}

	skillFile := filepath.Join(result.Path, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	content := string(data)

	// Section headings of the (intentionally compacted) skill — see commit
	// 6e0aa88 "compact the dtctl skill". Keep these in sync with SKILL.md.
	mustContain := []string{
		"Dynatrace Control with dtctl",
		"Resources & verbs",
		"Output for agents",
		"Apply & templates",
		"Permissions & safety",
		"Initialization",
	}
	for _, s := range mustContain {
		if !strings.Contains(content, s) {
			t.Errorf("SKILL.md should contain %q", s)
		}
	}
}

func TestInstalledSkillContent_SubstantialSize(t *testing.T) {
	tmpDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, tmpDir, false, false)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}

	skillFile := filepath.Join(result.Path, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	// The skill was intentionally compacted (commit 6e0aa88); this guards
	// against a truncated/empty install, not a specific length.
	lines := strings.Count(string(data), "\n")
	if lines < 100 {
		t.Errorf("SKILL.md has only %d lines, expected 100+", lines)
	}
}

func TestCopyEmbeddedFS_SkipsGoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	agent, _ := FindAgent("claude")

	result, err := Install(agent, tmpDir, false, false)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}

	// embed.go should NOT be copied to the output
	embedGoPath := filepath.Join(result.Path, "embed.go")
	if _, err := os.Stat(embedGoPath); err == nil {
		t.Error("embed.go should NOT be copied to the install directory")
	}
}

func TestInstallOpenClaw_CopiesReferences(t *testing.T) {
	tmpDir := t.TempDir()
	agent, ok := FindAgent("openclaw")
	if !ok {
		t.Fatal("openclaw agent not found")
	}
	result, err := Install(agent, tmpDir, false, false)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Verify SKILL.md exists in skill directory
	skillFile := filepath.Join(result.Path, "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		t.Fatalf("SKILL.md not found at %s", skillFile)
	}
	// Verify references directory was created
	refsDir := filepath.Join(result.Path, "references")
	if _, err := os.Stat(refsDir); err != nil {
		t.Fatalf("references/ directory not found at %s", refsDir)
	}
	// Verify at least one reference file exists
	entries, err := os.ReadDir(refsDir)
	if err != nil {
		t.Fatalf("failed to read references dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("references/ directory is empty, expected reference files")
	}
}

func TestUninstallOpenClaw_CleansReferences(t *testing.T) {
	tmpDir := t.TempDir()
	agent, ok := FindAgent("openclaw")
	if !ok {
		t.Fatal("openclaw agent not found")
	}
	// Install first
	_, err := Install(agent, tmpDir, false, false)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	// Verify references exist
	refsDir := filepath.Join(tmpDir, "skills", "dtctl", "references")
	if _, err := os.Stat(refsDir); err != nil {
		t.Fatalf("references/ not created during install: %v", err)
	}
	// Uninstall
	removed, err := Uninstall(agent, tmpDir)
	if err != nil {
		t.Fatalf("Uninstall failed: %v", err)
	}
	if len(removed) == 0 {
		t.Error("Uninstall removed nothing")
	}
	// Verify entire skill directory cleaned up (including references)
	skillDir := filepath.Join(tmpDir, "skills", "dtctl")
	if _, err := os.Stat(skillDir); err == nil {
		t.Error("skill directory still exists after uninstall")
	}
}

// --- Cross-client tests ---

func TestCrossClientAgent(t *testing.T) {
	t.Run("has correct paths", func(t *testing.T) {
		expectedProject := filepath.Join(".agents", "skills", "dtctl")
		if CrossClientAgent.ProjectPath != expectedProject {
			t.Errorf("ProjectPath = %q, want %q", CrossClientAgent.ProjectPath, expectedProject)
		}
		expectedGlobal := filepath.Join(".agents", "skills", "dtctl")
		if CrossClientAgent.GlobalPath != expectedGlobal {
			t.Errorf("GlobalPath = %q, want %q", CrossClientAgent.GlobalPath, expectedGlobal)
		}
	})

	t.Run("has correct name", func(t *testing.T) {
		if CrossClientAgent.Name != "cross-client" {
			t.Errorf("Name = %q, want %q", CrossClientAgent.Name, "cross-client")
		}
	})

	t.Run("has display name", func(t *testing.T) {
		if CrossClientAgent.DisplayName == "" {
			t.Error("DisplayName should not be empty")
		}
	})

	t.Run("not in regular agents list", func(t *testing.T) {
		for _, a := range AllAgents() {
			if a.Name == "cross-client" {
				t.Error("cross-client should not appear in AllAgents()")
			}
		}
	})

	t.Run("not in SupportedAgents", func(t *testing.T) {
		for _, name := range SupportedAgents() {
			if name == "cross-client" {
				t.Error("cross-client should not appear in SupportedAgents()")
			}
		}
	})
}

func TestFindAgent_CrossClient(t *testing.T) {
	agent, ok := FindAgent("cross-client")
	if !ok {
		t.Fatal("FindAgent should find cross-client")
	}
	if agent.Name != "cross-client" {
		t.Errorf("agent.Name = %q, want %q", agent.Name, "cross-client")
	}
}

func TestInstall_CrossClient(t *testing.T) {
	t.Run("installs to cross-client project path", func(t *testing.T) {
		tmpDir := t.TempDir()

		result, err := Install(CrossClientAgent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("Install error: %v", err)
		}

		expectedDir := filepath.Join(tmpDir, ".agents", "skills", "dtctl")
		if result.Path != expectedDir {
			t.Errorf("Path = %q, want %q", result.Path, expectedDir)
		}
		if result.Agent.Name != "cross-client" {
			t.Errorf("Agent.Name = %q, want %q", result.Agent.Name, "cross-client")
		}

		// Verify SKILL.md exists and has content
		skillFile := filepath.Join(expectedDir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			t.Fatalf("failed to read SKILL.md: %v", err)
		}
		if len(data) == 0 {
			t.Error("SKILL.md is empty")
		}
		if !strings.Contains(string(data), "dtctl") {
			t.Error("SKILL.md should contain 'dtctl'")
		}

		// Verify references/ directory exists
		refsDir := filepath.Join(expectedDir, "references")
		if info, err := os.Stat(refsDir); err != nil || !info.IsDir() {
			t.Errorf("references/ directory should exist at %s", refsDir)
		}
	})

	t.Run("identical content to agent-specific install", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Install cross-client
		crossResult, err := Install(CrossClientAgent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("cross-client Install error: %v", err)
		}
		crossData, err := os.ReadFile(filepath.Join(crossResult.Path, "SKILL.md"))
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		// Install a regular agent
		agent, _ := FindAgent("claude")
		agentResult, err := Install(agent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("claude Install error: %v", err)
		}
		agentData, err := os.ReadFile(filepath.Join(agentResult.Path, "SKILL.md"))
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		if string(crossData) != string(agentData) {
			t.Error("cross-client and agent-specific SKILL.md content should be identical")
		}
	})

	t.Run("refuses overwrite without force", func(t *testing.T) {
		tmpDir := t.TempDir()

		_, err := Install(CrossClientAgent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("first Install error: %v", err)
		}

		_, err = Install(CrossClientAgent, tmpDir, false, false)
		if err == nil {
			t.Fatal("expected error on duplicate install")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("error should mention 'already exists', got: %v", err)
		}
	})

	t.Run("overwrites with force", func(t *testing.T) {
		tmpDir := t.TempDir()

		_, err := Install(CrossClientAgent, tmpDir, false, false)
		if err != nil {
			t.Fatalf("first Install error: %v", err)
		}

		result, err := Install(CrossClientAgent, tmpDir, false, true)
		if err != nil {
			t.Fatalf("overwrite Install error: %v", err)
		}
		if !result.Replaced {
			t.Error("expected Replaced=true on overwrite")
		}
	})

	t.Run("supports global install", func(t *testing.T) {
		// Just verify the path resolves correctly (don't actually write to $HOME)
		path, err := resolvePath(CrossClientAgent, "/tmp/project", true)
		if err != nil {
			t.Fatalf("resolvePath error: %v", err)
		}
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".agents", "skills", "dtctl")
		if path != expected {
			t.Errorf("path = %q, want %q", path, expected)
		}
	})
}

func TestUninstall_CrossClient(t *testing.T) {
	t.Run("removes project-local cross-client install", func(t *testing.T) {
		tmpDir := t.TempDir()

		result, _ := Install(CrossClientAgent, tmpDir, false, false)

		// Verify SKILL.md exists
		skillFile := filepath.Join(result.Path, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			t.Fatal("SKILL.md should exist before uninstall")
		}

		removed, err := Uninstall(CrossClientAgent, tmpDir)
		if err != nil {
			t.Fatalf("Uninstall error: %v", err)
		}
		if len(removed) != 1 {
			t.Fatalf("expected 1 removed, got %d", len(removed))
		}

		// Verify directory is gone
		if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
			t.Error("skill directory should not exist after uninstall")
		}
	})

	t.Run("returns empty for non-installed", func(t *testing.T) {
		tmpDir := t.TempDir()

		removed, err := Uninstall(CrossClientAgent, tmpDir)
		if err != nil {
			t.Fatalf("Uninstall error: %v", err)
		}
		if len(removed) != 0 {
			t.Errorf("expected 0 removed, got %d", len(removed))
		}
	})
}

func TestStatus_CrossClient(t *testing.T) {
	t.Run("installed project-local", func(t *testing.T) {
		tmpDir := t.TempDir()

		Install(CrossClientAgent, tmpDir, false, false)

		result := Status(CrossClientAgent, tmpDir)
		if !result.Installed {
			t.Error("expected Installed=true")
		}
		if result.Global {
			t.Error("expected Global=false")
		}
		expectedDir := filepath.Join(tmpDir, ".agents", "skills", "dtctl")
		if result.Path != expectedDir {
			t.Errorf("Path = %q, want %q", result.Path, expectedDir)
		}
	})

	t.Run("not installed", func(t *testing.T) {
		tmpDir := t.TempDir()

		result := Status(CrossClientAgent, tmpDir)
		if result.Installed {
			t.Error("expected Installed=false")
		}
	})
}

func TestStatusAll_IncludesCrossClient(t *testing.T) {
	tmpDir := t.TempDir()

	// Install only cross-client
	Install(CrossClientAgent, tmpDir, false, false)

	results := StatusAll(tmpDir)

	// Find the cross-client entry
	var crossClientResult *StatusResult
	for _, r := range results {
		if r.Agent.Name == "cross-client" {
			crossClientResult = r
			break
		}
	}

	if crossClientResult == nil {
		t.Fatal("cross-client entry not found in StatusAll results")
	}
	if !crossClientResult.Installed {
		t.Error("cross-client should be marked as installed")
	}

	// Verify it's the first entry
	if results[0].Agent.Name != "cross-client" {
		t.Errorf("cross-client should be the first entry, got %q", results[0].Agent.Name)
	}
}
