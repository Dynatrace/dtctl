// Package skills provides installation and management of AI coding assistant
// skill files for dtctl. It copies the embedded skill directory (SKILL.md +
// references/) to the appropriate agent-specific location following the
// agentskills.io open standard.
package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
	dtctlskill "github.com/Dynatrace/dtctl/skills/dtctl"
)

// Agent represents a supported AI coding assistant.
type Agent struct {
	// Name is the canonical identifier (e.g. "claude", "copilot").
	Name string
	// DisplayName is the human-readable name (e.g. "Claude Code").
	DisplayName string
	// ProjectPath is the relative path from the project root for project-local install.
	// This points to the skill *directory* (e.g. ".claude/skills/dtctl").
	ProjectPath string
	// GlobalPath is the relative path from the user's home directory for global install.
	// Empty means global install is not supported.
	// This points to the skill *directory* (e.g. ".claude/skills/dtctl").
	GlobalPath string
	// EnvVar is the environment variable used to detect this agent.
	EnvVar string
	// DetectName is the name returned by aidetect.Detect() for this agent.
	DetectName string
}

// CrossClientAgent is a pseudo-agent representing the cross-client shared
// directory defined by the agentskills.io convention. Skills installed here
// are automatically discovered by any agent that scans ~/.agents/skills/ or
// <project>/.agents/skills/.
var CrossClientAgent = Agent{
	Name:        "cross-client",
	DisplayName: "Cross-Client (agentskills.io)",
	ProjectPath: filepath.Join(".agents", "skills", "dtctl"),
	GlobalPath:  filepath.Join(".agents", "skills", "dtctl"),
	EnvVar:      "",
	DetectName:  "",
}

// agents is the registry of all supported AI coding assistants.
// All agents follow the agentskills.io standard: <client>/skills/<name>/SKILL.md
var agents = []Agent{
	{
		Name:        "claude",
		DisplayName: "Claude Code",
		ProjectPath: filepath.Join(".claude", "skills", "dtctl"),
		GlobalPath:  filepath.Join(".claude", "skills", "dtctl"),
		EnvVar:      "CLAUDECODE",
		DetectName:  "claude-code",
	},
	{
		Name:        "codex",
		DisplayName: "OpenAI Codex CLI",
		ProjectPath: filepath.Join(".codex", "skills", "dtctl"),
		GlobalPath:  filepath.Join(".codex", "skills", "dtctl"),
		EnvVar:      "CODEX",
		DetectName:  "codex",
	},
	{
		Name:        "copilot",
		DisplayName: "GitHub Copilot",
		ProjectPath: filepath.Join(".github", "skills", "dtctl"),
		GlobalPath:  filepath.Join(".copilot", "skills", "dtctl"),
		EnvVar:      "GITHUB_COPILOT",
		DetectName:  "github-copilot",
	},
	{
		Name:        "cursor",
		DisplayName: "Cursor",
		ProjectPath: filepath.Join(".cursor", "skills", "dtctl"),
		GlobalPath:  "",
		EnvVar:      "CURSOR_AGENT",
		DetectName:  "cursor",
	},
	{
		Name:        "kiro",
		DisplayName: "Kiro",
		ProjectPath: filepath.Join(".kiro", "skills", "dtctl"),
		GlobalPath:  "",
		EnvVar:      "KIRO",
		DetectName:  "kiro",
	},
	{
		Name:        "junie",
		DisplayName: "Junie",
		ProjectPath: filepath.Join(".junie", "skills", "dtctl"),
		GlobalPath:  filepath.Join(".junie", "skills", "dtctl"),
		EnvVar:      "JUNIE",
		DetectName:  "junie",
	},
	{
		Name:        "opencode",
		DisplayName: "OpenCode",
		ProjectPath: filepath.Join(".opencode", "skills", "dtctl"),
		GlobalPath:  filepath.Join(".config", "opencode", "skills", "dtctl"),
		EnvVar:      "OPENCODE",
		DetectName:  "opencode",
	},
	{
		Name:        "openclaw",
		DisplayName: "OpenClaw",
		ProjectPath: filepath.Join("skills", "dtctl"),
		GlobalPath:  filepath.Join(".openclaw", "workspace", "skills", "dtctl"),
		EnvVar:      "OPENCLAW",
		DetectName:  "openclaw",
	},
}

// InstallResult describes the outcome of an install operation.
type InstallResult struct {
	Agent    Agent
	Path     string
	Global   bool
	Replaced bool
}

// StatusResult describes the current installation state for an agent.
type StatusResult struct {
	Agent     Agent
	Installed bool
	Path      string
	Global    bool
}

// SupportedAgents returns the list of all supported agent names
// (excluding the cross-client pseudo-agent).
func SupportedAgents() []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return names
}

// AllAgents returns the full agent registry (excluding the cross-client
// pseudo-agent).
func AllAgents() []Agent {
	return agents
}

// FindAgent looks up an agent by canonical name. Also matches "cross-client".
func FindAgent(name string) (Agent, bool) {
	if name == CrossClientAgent.Name {
		return CrossClientAgent, true
	}
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// DetectAgent uses aidetect to find the current agent and maps it to our registry.
func DetectAgent() (Agent, bool) {
	info := aidetect.Detect()
	if !info.Detected {
		return Agent{}, false
	}
	for _, a := range agents {
		if a.DetectName == info.Name {
			return a, true
		}
	}
	return Agent{}, false
}

// skillFileName is the SKILL.md filename used for installation checks.
const skillFileName = "SKILL.md"

// Install copies the embedded skill directory (SKILL.md + references/) into
// the agent's skill directory. If global is true, it installs to the user's
// home directory; otherwise to the project root (baseDir). It returns an
// error if the directory already exists and overwrite is false.
func Install(agent Agent, baseDir string, global bool, overwrite bool) (*InstallResult, error) {
	skillDir, err := resolvePath(agent, baseDir, global)
	if err != nil {
		return nil, err
	}

	skillFile := filepath.Join(skillDir, skillFileName)
	replaced := false
	if _, err := os.Stat(skillFile); err == nil {
		if !overwrite {
			return nil, fmt.Errorf("skill file already exists at %s (use --force to overwrite)", skillFile)
		}
		replaced = true
	}

	// Remove existing skill directory if replacing, to avoid stale files
	if replaced {
		if err := os.RemoveAll(skillDir); err != nil {
			return nil, fmt.Errorf("failed to remove existing skill directory %s: %w", skillDir, err)
		}
	}

	// Copy the entire embedded filesystem to the skill directory
	if err := copyEmbeddedFS(dtctlskill.Content, skillDir); err != nil {
		return nil, fmt.Errorf("failed to install skill files: %w", err)
	}

	return &InstallResult{
		Agent:    agent,
		Path:     skillDir,
		Global:   global,
		Replaced: replaced,
	}, nil
}

// copyEmbeddedFS copies all files from the embedded filesystem to destDir,
// preserving the directory structure.
func copyEmbeddedFS(embeddedFS fs.FS, destDir string) error {
	return fs.WalkDir(embeddedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip the embed.go file (it's Go source, not skill content)
		if filepath.Ext(path) == ".go" {
			return nil
		}

		destPath := filepath.Join(destDir, path)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}

		data, err := fs.ReadFile(embeddedFS, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		dir := filepath.Dir(destPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}

		return os.WriteFile(destPath, data, 0o644)
	})
}

// Uninstall removes installed skill directories. It checks both project-local
// and global locations and removes any that exist. Returns the paths that were
// successfully removed. If some removals fail, the successfully removed paths
// are still returned alongside the error.
func Uninstall(agent Agent, baseDir string) ([]string, error) {
	var removed []string
	var errs []string

	// Check project-local
	projectDir := filepath.Join(baseDir, agent.ProjectPath)
	if _, err := os.Stat(projectDir); err == nil {
		if err := os.RemoveAll(projectDir); err != nil {
			errs = append(errs, fmt.Sprintf("failed to remove %s: %v", projectDir, err))
		} else {
			removed = append(removed, projectDir)
		}
	}

	// Check global
	if agent.GlobalPath != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			errs = append(errs, fmt.Sprintf("failed to determine home directory: %v", err))
		} else {
			globalDir := filepath.Join(home, agent.GlobalPath)
			if _, err := os.Stat(globalDir); err == nil {
				if err := os.RemoveAll(globalDir); err != nil {
					errs = append(errs, fmt.Sprintf("failed to remove %s: %v", globalDir, err))
				} else {
					removed = append(removed, globalDir)
				}
			}
		}
	}

	if len(errs) > 0 {
		return removed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return removed, nil
}

// Status checks the installation state for a given agent.
func Status(agent Agent, baseDir string) *StatusResult {
	// Check project-local first — look for SKILL.md inside the skill directory
	projectDir := filepath.Join(baseDir, agent.ProjectPath)
	skillFile := filepath.Join(projectDir, skillFileName)
	if _, err := os.Stat(skillFile); err == nil {
		return &StatusResult{
			Agent:     agent,
			Installed: true,
			Path:      projectDir,
			Global:    false,
		}
	}

	// Check global (best-effort: if home dir lookup fails, treat as not installed)
	if agent.GlobalPath != "" {
		if home, err := os.UserHomeDir(); err == nil {
			globalDir := filepath.Join(home, agent.GlobalPath)
			globalSkillFile := filepath.Join(globalDir, skillFileName)
			if _, err := os.Stat(globalSkillFile); err == nil {
				return &StatusResult{
					Agent:     agent,
					Installed: true,
					Path:      globalDir,
					Global:    true,
				}
			}
		}
	}

	return &StatusResult{
		Agent:     agent,
		Installed: false,
	}
}

// StatusAll checks installation state for all supported agents and the
// cross-client location.
func StatusAll(baseDir string) []*StatusResult {
	results := make([]*StatusResult, 0, len(agents)+1)
	results = append(results, Status(CrossClientAgent, baseDir))
	for _, a := range agents {
		results = append(results, Status(a, baseDir))
	}
	return results
}

// resolvePath determines the absolute path for the skill directory.
func resolvePath(agent Agent, baseDir string, global bool) (string, error) {
	if global {
		if agent.GlobalPath == "" {
			return "", fmt.Errorf("%s does not support global installation", agent.DisplayName)
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to determine home directory: %w", err)
		}
		return filepath.Join(home, agent.GlobalPath), nil
	}
	return filepath.Join(baseDir, agent.ProjectPath), nil
}
