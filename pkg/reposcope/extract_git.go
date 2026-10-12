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

// readRemote reads the origin remote's repository name from .git/config as a
// discovery token.
func readRemote(fsys fs.FS) []sourceNames {
	url, line, ok := originURL(fsys)
	if !ok {
		return nil
	}
	if t, ok := newToken(kindRemote, repositoryName(url), gitConfig, line); ok {
		return []sourceNames{{dir: ".", tokens: []token{t}}}
	}
	return nil
}

// RepositoryName is the origin remote's repository name, as its hosting
// service spells it, for fsys the repository root; empty when there is no
// origin to read. It is what a clone is called on every machine, unlike the
// directory it was cloned into.
func RepositoryName(fsys fs.FS) string {
	url, _, ok := originURL(fsys)
	if !ok {
		return ""
	}
	return repositoryName(url)
}

// originURL is the origin remote's URL in .git/config, with its line. When
// .git is a file (a worktree or a submodule) the configuration lives outside
// the repository root fsys is limited to, and there is none.
func originURL(fsys fs.FS) (url string, line int, ok bool) {
	data, err := readRegular(fsys, gitConfig)
	if err != nil {
		return "", 0, false
	}
	section := ""
	for i, text := range strings.Split(string(data), "\n") {
		if m := gitSectionRe.FindStringSubmatch(text); m != nil {
			section = m[1]
			continue
		}
		if m := gitURLRe.FindStringSubmatch(text); m != nil && section == `remote "origin"` {
			return m[1], i + 1, true
		}
	}
	return "", 0, false
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
