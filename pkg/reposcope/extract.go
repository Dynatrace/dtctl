package reposcope

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
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

// skippedDirs are never entered: dependency trees, build output, build
// tooling and tool state hold other projects' names, not this repository's.
// Gradle's buildSrc is a build of its own that compiles the main build's
// logic, so like target and build it names nothing that runs.
var skippedDirs = keySet(".git", "node_modules", "vendor", "target", "build", "dist",
	".venv", "__pycache__", ".idea", ".terraform", ".gradle", "buildSrc")

// excludedDirs hold test content, compared case-insensitively and behind a
// leading dot, so .NET's Tests/ and Helm's rendered .tests/ count too: Go's
// testdata, Jest's __tests__, end-to-end suites, and examples, whose
// manifests deploy a demonstration rather than the service.
// Unlike skippedDirs they are entered when discovery was asked about a
// directory inside one (see onPath). dev is deliberately absent: it names
// an environment, and deploy/overlays/dev describes exactly what runs on the
// environment developers use most.
var excludedDirs = union(testWords, "testdata", "__tests__", "e2e", "examples")

// testFileWords mark a test's own file beside a real one when they are a
// whole word of its name: Dockerfile.test, deployment-e2e.yaml,
// docker-compose.test.yml.
var testFileWords = union(testWords, "e2e")

// source is one kind of file names are read from. A build file or Dockerfile
// makes its directory a unit, except inside a Gradle build (see
// gradleBuilds.absorbs); a Kubernetes manifest or Helm chart does not, and is
// attributed to a unit instead (see attribute).
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
	{named(gradleSettingsFiles...), true, buildFile(gradleRootProject)},
	{named(gradleBuildFiles...), true, buildFile(gradleArchivesName)},
	{named("pyproject.toml"), true, buildFile(pyprojectName)},
	{func(n string) bool { return strings.HasSuffix(n, ".csproj") }, true, buildFile(assemblyName)},
	{isDockerfile, true, buildFile(dockerEntrypoint)},
	{named("Chart.yaml"), false, chartName},
	{func(n string) bool { return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") }, false, manifestWorkloads},
}

func named(names ...string) func(string) bool {
	return func(name string) bool { return slices.Contains(names, name) }
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
	// dropped are the unit's names held back as test names (see
	// token.testName), sorted, so that holding them back is never silent.
	dropped []string
	// sharedBy are the Gradle deployables that share the library module
	// discovery ran for; nothing is sent for the module itself.
	sharedBy []string
}

// extract reads the names the repository at fsys gives the unit holding dir,
// walking within maxDepth, maxFiles and maxFileBytes, never entering
// skippedDirs, and leaving test content unread: excludedDirs other than those
// on the way to dir, and test files (see testFile). Nothing leaves the
// machine here.
//
// The unit is the nearest directory at or above dir that holds a build file
// or a Dockerfile, or the repository root when none does; inside a Gradle
// build it is a deployable or the build (see gradleBuild). Its directory name
// is a token, and so is dir's own name when the unit is a root no Gradle
// build holds: in a monorepo with one build file at the top, the directory a
// developer stands in is often what the service is called, while a Gradle
// module is part of what its build deploys.
func extract(fsys fs.FS, dir string) (*signals, error) {
	dir = path.Clean(dir)
	files, err := listSources(fsys, dir, func(f string) bool { return sourceFor(f) != nil })
	if err != nil {
		return nil, err
	}
	var found []sourceNames
	gradle := map[string]string{}
	for _, f := range files {
		data, err := readRegular(fsys, f)
		if err != nil {
			continue
		}
		found = append(found, sourceFor(f).read(data, f)...)
		if isGradleFile(path.Base(f)) {
			gradle[f] = string(data)
		}
	}
	found = append(found, readRemote(fsys)...)

	builds := readGradleBuilds(files, gradle, found)
	units := map[string]bool{".": true}
	for _, f := range files {
		if sourceFor(f).makesUnit && !builds.absorbs(f) {
			units[path.Dir(f)] = true
		}
	}
	for _, b := range builds {
		if deployables := b.deployables(); len(deployables) > 1 {
			for _, d := range deployables {
				units[d] = true
			}
		}
	}

	unit, build := nearestUnit(units, dir), builds.of(dir)
	if build == nil {
		nameDir := unit
		if nameDir == "." {
			nameDir = dir
		}
		return newSignals(unit, unitTokens(units, found, unit, nameDir)), nil
	}
	unit, sharedBy := build.unitOf(dir, unit)
	if len(sharedBy) > 0 {
		return &signals{unit: unit, sharedBy: sharedBy}, nil
	}
	return newSignals(unit, unitTokens(units, found, unit, unit)), nil
}

// unitTokens collects the names of unit: nameDir's own name unless it is the
// repository root, then the names of every source attributed to the unit.
func unitTokens(units map[string]bool, found []sourceNames, unit, nameDir string) []token {
	var tokens []token
	if nameDir != "." {
		if t, ok := newToken(kindDir, path.Base(nameDir), dirSource(nameDir), 0); ok {
			tokens = append(tokens, t)
		}
	}
	for _, f := range found {
		if attribute(units, f) == unit {
			tokens = append(tokens, f.tokens...)
		}
	}
	return tokens
}

// newSignals holds the unit's test names back from its tokens. They are held
// back here rather than where they are read, so that what is reported as held
// back is this unit's, not every unit's.
func newSignals(unit string, tokens []token) *signals {
	s := &signals{unit: unit}
	dropped := map[string]bool{}
	for _, t := range tokens {
		// A namespace is never sent, only matched, so holding one back
		// would only take it out of the evidence.
		if t.kind != kindNamespace && t.testName() {
			dropped[t.value] = true
		} else {
			s.tokens = append(s.tokens, t)
		}
	}
	if len(dropped) > 0 {
		s.dropped = sortedKeys(dropped)
	}
	return s
}

// dirSource is the source recorded for a directory's own name: the directory
// with a trailing slash, which no file read from it can be.
func dirSource(dir string) string {
	return dir + "/"
}

// listSources lists the files keep accepts, for discovery from dir. Only
// those count toward maxFiles.
func listSources(fsys fs.FS, dir string, keep func(string) bool) ([]string, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		switch {
		case walkErr != nil:
			if p == "." {
				return walkErr
			}
			return nil
		case d.IsDir():
			if p != "." && (skippedDirs[d.Name()] || strings.Count(p, "/")+1 > maxDepth ||
				(excludedDirs[strings.ToLower(strings.TrimPrefix(d.Name(), "."))] && !onPath(p, dir))) {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular() || !keep(p) || testFile(d.Name()):
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

// onPath reports whether p is dir or one of its ancestors. Discovery asked
// about a directory reads it whatever it is called: a repository that keeps
// its deployables under examples/<name> still discovers from
// examples/checkout.
func onPath(p, dir string) bool {
	return p == dir || strings.HasPrefix(dir, p+"/")
}

// testFile reports whether a file is a test's own: a word of its name, split
// at '.', '-' and '_', is in testFileWords (Dockerfile.test,
// deployment-e2e.yaml), or a word after the first is spec
// (checkout.spec.yaml). spec counts only after the first word, because a
// manifest may well be called spec.yaml.
func testFile(name string) bool {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	for i, w := range words {
		if testFileWords[w] || (i > 0 && w == "spec") {
			return true
		}
	}
	return false
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
