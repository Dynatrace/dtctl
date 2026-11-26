package skills

import (
	"os"
	"path/filepath"
)

// CompanionInstallCommand installs the Dynatrace for AI skill collection
// (https://github.com/Dynatrace/dynatrace-for-ai), which adds the DQL and
// observability domain knowledge the dtctl skill deliberately leaves out.
const CompanionInstallCommand = "npx skills add dynatrace/dynatrace-for-ai"

// companionMarkerSkill is the Dynatrace for AI skill whose presence is taken
// as evidence that the collection is installed. It is the one every dtctl DQL
// workflow leans on, so it is the one worth checking for.
const companionMarkerSkill = "dt-dql-essentials"

// CompanionInstalled reports whether the Dynatrace for AI skills appear to be
// installed where the agent would discover them next to dtctl: the skills
// directory holding installedPath, plus the agent's project-local and global
// skills directories. Symlinked skill directories (as `npx skills add`
// creates) count. Detection is best-effort: a location that cannot be
// resolved or read is treated as not having them.
func CompanionInstalled(agent Agent, baseDir, installedPath string) bool {
	roots := []string{filepath.Dir(installedPath)}
	if agent.ProjectPath != "" {
		roots = append(roots, filepath.Dir(filepath.Join(baseDir, agent.ProjectPath)))
	}
	if agent.GlobalPath != "" {
		if home, err := os.UserHomeDir(); err == nil {
			roots = append(roots, filepath.Dir(filepath.Join(home, agent.GlobalPath)))
		}
	}
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, companionMarkerSkill, skillFileName)); err == nil {
			return true
		}
	}
	return false
}
