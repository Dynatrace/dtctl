package reposcope

import (
	"io/fs"
	"regexp"
	"strings"
)

// The origin remote names the repository the way its hosting service does,
// which is often the name a team deploys it under. It is read as a file: no
// git subprocess, so nothing here needs more than fsys.

var (
	gitSectionRe = regexp.MustCompile(`^\s*\[\s*([^\]]+?)\s*\]\s*$`)
	gitURLRe     = regexp.MustCompile(`^\s*url\s*=\s*(\S+)\s*$`)
)

const gitConfig = ".git/config"

// readRemote reads the origin remote's repository name from .git/config. When
// .git is a file (a worktree or a submodule) the configuration lives outside
// the repository root fsys is limited to, and there is no remote token.
func readRemote(fsys fs.FS) []sourceNames {
	data, err := readRegular(fsys, gitConfig)
	if err != nil {
		return nil
	}
	section := ""
	for i, line := range strings.Split(string(data), "\n") {
		if m := gitSectionRe.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if m := gitURLRe.FindStringSubmatch(line); m != nil && section == `remote "origin"` {
			if t, ok := newToken(kindRemote, repositoryName(m[1]), gitConfig, i+1); ok {
				return []sourceNames{{dir: ".", tokens: []token{t}}}
			}
			return nil
		}
	}
	return nil
}

// repositoryName is the last path segment of a remote URL without ".git",
// for both URL ("https://host/org/repo.git") and scp ("git@host:repo.git")
// forms.
func repositoryName(url string) string {
	name := strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	return name
}
