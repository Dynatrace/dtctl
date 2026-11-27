package reposcope

import (
	"io/fs"
	"net/url"
	"regexp"
	"strings"
)

// The origin remote names the repository the way its hosting service does,
// which is often the name a team deploys it under. It is read as a file: no
// git subprocess, so nothing here needs more than fsys.

// A section header, with the comment git allows after it: [remote "origin"].
// The section's name is spelled in any case, the subsection's in one. A key
// and its value follow on lines of their own; the value is bare up to a
// comment, or quoted, and then a comment character is part of it.
var (
	gitSectionRe = regexp.MustCompile(`^\[\s*([A-Za-z0-9.-]+)(?:\s+"([^"\\]*)")?\s*\]\s*(?:[#;].*)?$`)
	gitURLRe     = regexp.MustCompile(`(?i)^url\s*=\s*(?:"([^"\\]*)"|([^"#;\s][^"#;]*?)?)\s*(?:[#;].*)?$`)
)

const gitConfig = ".git/config"

// readRemote reads the origin remote's repository name from .git/config as a
// discovery token.
func readRemote(fsys fs.FS) []sourceNames {
	remote, line, ok := originURL(fsys)
	if !ok {
		return nil
	}
	if t, ok := newToken(kindRemote, repositoryName(remote), gitConfig, line); ok {
		t.indirect = indirectRemote
		return []sourceNames{{dir: ".", tokens: []token{t}}}
	}
	return nil
}

// RepositoryName is the origin remote's repository name, as its hosting
// service spells it, for fsys the repository root; empty when there is no
// origin to read. It is what a clone is called on every machine, unlike the
// directory it was cloned into.
func RepositoryName(fsys fs.FS) string {
	remote, _, ok := originURL(fsys)
	if !ok {
		return ""
	}
	return repositoryName(remote)
}

// originURL is the origin remote's URL in .git/config, with its line. When
// .git is a file (a worktree or a submodule) the configuration lives outside
// the repository root fsys is limited to, and there is none. Nor is there one
// when the URL is written in a way this reader does not understand, an escape
// or a line continuation among them: a caller then falls back to what it has
// without the remote.
func originURL(fsys fs.FS) (remote string, line int, ok bool) {
	data, err := readSource(fsys, gitConfig)
	if err != nil {
		return "", 0, false
	}
	origin := false
	for i, text := range strings.Split(string(data), "\n") {
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "[") {
			// A header that is not understood still ends the section before
			// it: what follows is not origin's.
			m := gitSectionRe.FindStringSubmatch(text)
			origin = m != nil && strings.EqualFold(m[1], "remote") && m[2] == "origin"
			continue
		}
		if m := gitURLRe.FindStringSubmatch(text); m != nil && origin {
			return m[1] + m[2], i + 1, true
		}
	}
	return "", 0, false
}

// repositoryName is the last segment of a remote's path without ".git", or
// "" when the remote names no repository. Only the path is read: a remote may
// carry credentials, a query string or a fragment, and none of them is a name.
//
// A remote with a scheme ("https://host/org/repo.git") is parsed as a URL. Any
// other is git's scp form ("git@host:org/repo.git"), whose path follows the
// first colon when no slash comes before it, or a local path. A path written
// with backslashes names no repository: its segments are a machine's
// directories, a user's among them.
func repositoryName(remote string) string {
	p := remote
	switch {
	case strings.ContainsRune(remote, '\\'):
		return ""
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		p = u.Path
	default:
		if i := strings.IndexByte(remote, ':'); i >= 0 && !strings.ContainsRune(remote[:i], '/') {
			p = remote[i+1:]
		}
	}
	p = strings.TrimSuffix(strings.TrimRight(p, "/"), ".git")
	return p[strings.LastIndexByte(p, '/')+1:]
}
