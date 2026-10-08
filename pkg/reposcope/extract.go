package reposcope

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Discovery runs inside a developer's checkout, which can be anything from one
// service to a monorepo with a vendored world, so the walk is bounded rather
// than trusted to be small.
const (
	maxDepth     = 6       // directories below the root
	maxFiles     = 2000    // files a source matches, every .yaml/.yml among them
	maxFileBytes = 1 << 20 // per file
)

// skippedDirs are never entered: dependency trees, build output and tool
// state hold other projects' names, not this repository's.
var skippedDirs = keySet(".git", "node_modules", "vendor", "target", "build", "dist",
	".venv", "__pycache__", ".idea", ".terraform")

// source is one kind of file names are read from. A build file or Dockerfile
// makes its directory a unit; a Kubernetes manifest or Helm chart does not,
// and is attributed to a unit instead (see attribute).
type source struct {
	match     func(name string) bool
	makesUnit bool
	read      func(data []byte, file string) []sourceNames
}

// sources is checked in order; the first match reads the file.
var sources = []source{
	{named("go.mod"), true, buildFile(goModule)},
	{named("package.json"), true, buildFile(packageName)},
	{named("pom.xml"), true, buildFile(pomArtifact)},
	{named("pyproject.toml"), true, buildFile(pyprojectName)},
	{func(n string) bool { return strings.HasSuffix(n, ".csproj") }, true, buildFile(assemblyName)},
	{isDockerfile, true, buildFile(dockerEntrypoint)},
	{named("Chart.yaml"), false, chartName},
	{func(n string) bool { return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") }, false, manifestWorkloads},
}

func named(want string) func(string) bool {
	return func(name string) bool { return name == want }
}

func isDockerfile(name string) bool {
	return name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.") || strings.HasSuffix(name, ".Dockerfile")
}

func sourceFor(file string) *source {
	name := path.Base(file)
	for i := range sources {
		if sources[i].match(name) {
			return &sources[i]
		}
	}
	return nil
}

// sourceNames is what one source contributed: tokens that belong together,
// the directory they were found in, and the names that may claim a unit for
// them (a manifest's workload and image names).
type sourceNames struct {
	dir    string
	claims []string
	tokens []token
}

type signals struct {
	unit   string
	tokens []token
}

// extract reads the names the repository at fsys gives the unit holding dir,
// walking within maxDepth, maxFiles and maxFileBytes and never entering
// skippedDirs. Nothing leaves the machine here.
//
// The unit is the nearest directory at or above dir that holds a build file
// or a Dockerfile, or the repository root when none does. Its directory name
// is a token, and so is dir's own name when the unit is the root: in a
// monorepo with one build file at the top, the directory a developer stands
// in is often what the service is called.
func extract(fsys fs.FS, dir string) (*signals, error) {
	dir = path.Clean(dir)
	files, err := listSources(fsys)
	if err != nil {
		return nil, err
	}
	units := map[string]bool{".": true}
	for _, f := range files {
		if sourceFor(f).makesUnit {
			units[path.Dir(f)] = true
		}
	}
	s := &signals{unit: nearestUnit(units, dir)}
	nameDir := s.unit
	if nameDir == "." {
		nameDir = dir
	}
	if nameDir != "." {
		if t, ok := newToken(kindDir, path.Base(nameDir), dirSource(nameDir), 0); ok {
			s.tokens = append(s.tokens, t)
		}
	}

	var found []sourceNames
	for _, f := range files {
		if data, err := readRegular(fsys, f); err == nil {
			found = append(found, sourceFor(f).read(data, f)...)
		}
	}
	found = append(found, readRemote(fsys)...)
	for _, f := range found {
		if attribute(units, f) == s.unit {
			s.tokens = append(s.tokens, f.tokens...)
		}
	}
	return s, nil
}

// dirSource is the source recorded for a directory's own name: the directory
// with a trailing slash, which no file read from it can be.
func dirSource(dir string) string {
	return dir + "/"
}

func listSources(fsys fs.FS) ([]string, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		switch {
		case walkErr != nil:
			if p == "." {
				return walkErr
			}
			return nil
		case d.IsDir():
			if p != "." && (skippedDirs[d.Name()] || strings.Count(p, "/")+1 > maxDepth) {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular() || sourceFor(p) == nil:
			return nil
		case len(files) == maxFiles:
			return fs.SkipAll
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read repository: %w", err)
	}
	return files, nil
}

func nearestUnit(units map[string]bool, dir string) string {
	for d := dir; d != "." && d != "/"; d = path.Dir(d) {
		if units[d] {
			return d
		}
	}
	return "."
}

// attribute picks the unit a source's names belong to: the one unit named
// like one of its claims, so deploy/checkout/deployment.yaml lands on
// services/checkout, else the nearest unit above where they were found.
func attribute(units map[string]bool, f sourceNames) string {
	for _, claim := range f.claims {
		var named []string
		for u := range units {
			if u != "." && sameName(path.Base(u), claim) {
				named = append(named, u)
			}
		}
		if len(named) == 1 {
			return named[0]
		}
	}
	return nearestUnit(units, f.dir)
}

// buildFile adapts a reader of one name to a source: a build file's name
// always belongs to the unit it was found in.
func buildFile(read func(text, file string) (token, bool)) func([]byte, string) []sourceNames {
	return func(data []byte, file string) []sourceNames {
		if t, ok := read(string(data), file); ok {
			return []sourceNames{{dir: path.Dir(file), tokens: []token{t}}}
		}
		return nil
	}
}

// location renders where a token was read, "file:line", or the file alone
// when the line is unknown.
func location(file string, line int) string {
	if line <= 0 {
		return file
	}
	return fmt.Sprintf("%s:%d", file, line)
}

// tokenAt builds a token whose raw name was found at needle in text.
func tokenAt(kind, raw, file, text, needle string) (token, bool) {
	line := 0
	if i := strings.Index(text, needle); i >= 0 {
		line = strings.Count(text[:i], "\n") + 1
	}
	return newToken(kind, raw, file, line)
}
