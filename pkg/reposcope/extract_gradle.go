package reposcope

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// A Gradle build is one settings file and the modules under it, built
// together: most modules are libraries of the one or few that are deployed.
// A unit is therefore what is deployed, not every module. A build with one
// deployable is one unit at its settings file, however many modules it has;
// a build with several makes each deployable a unit, and a library belongs
// to the deployable that depends on it. A build file with no settings file
// at or above it is a build of its own, and a unit.

// The files a Gradle build is made of, in the Groovy and the Kotlin DSL.
var (
	gradleSettingsFiles = []string{"settings.gradle", "settings.gradle.kts"}
	gradleBuildFiles    = []string{"build.gradle", "build.gradle.kts"}
)

func isGradleFile(name string) bool {
	return slices.Contains(gradleSettingsFiles, name) || slices.Contains(gradleBuildFiles, name)
}

// The project references read from a build, in either DSL: a settings file's
// include(":a", ":b") or include ':a', ':b', and a module's project(":a"),
// project(path: ":a") (Groovy), project(path = ":a") (Kotlin) or type-safe
// projects.a. Only whole-line comments are skipped, and so are dependencies
// declared in a test configuration (testImplementation, testRuntimeOnly):
// a module only a deployable's tests use is not part of what it deploys. A
// projectDir remap is not read, so a remapped module is found by its
// directory alone.
var (
	gradleIncludeRe  = regexp.MustCompile(`(?m)^[ \t]*include(?:[ \t]*\(([^)]*)\)|[ \t]+(.+))`)
	gradleQuotedRe   = regexp.MustCompile(`["']([^"']+)["']`)
	gradleProjectRe  = regexp.MustCompile(`\bproject\(\s*(?:path\s*[:=]\s*)?["']([^"']+)["']`)
	gradleAccessorRe = regexp.MustCompile(`\bprojects\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)`)
	gradleCommentRe  = regexp.MustCompile(`(?m)^[ \t]*//.*$`)
	gradleTestDepRe  = regexp.MustCompile(`(?m)^[ \t]*test\w*\b.*$`)
)

// gradleBuild is one settings file's build: its modules (included, or a
// directory with a build file) with the modules each references, and which
// of them are deployed.
type gradleBuild struct {
	dir        string
	modules    map[string][]string
	deployable map[string]bool
}

// gradleBuilds are a repository's builds, by the directory of their settings
// file.
type gradleBuilds map[string]*gradleBuild

// readGradleBuilds reads the builds among files, text holding each Gradle
// file's content. A module is deployed when it holds a Dockerfile, or holds
// or is the one module claimed by name by a Helm chart or workload manifest
// (found), unless it is named like a test (see testModule).
func readGradleBuilds(files []string, text map[string]string, found []sourceNames) gradleBuilds {
	builds := gradleBuilds{}
	for _, f := range files {
		if slices.Contains(gradleSettingsFiles, path.Base(f)) {
			builds[path.Dir(f)] = &gradleBuild{dir: path.Dir(f), modules: map[string][]string{}, deployable: map[string]bool{}}
		}
	}
	if len(builds) == 0 {
		return builds
	}
	for _, f := range files {
		dir := path.Dir(f)
		b := builds.of(dir)
		if b == nil {
			continue
		}
		switch name := path.Base(f); {
		case slices.Contains(gradleSettingsFiles, name):
			for _, m := range gradleIncludeRe.FindAllStringSubmatch(gradleText(text[f]), -1) {
				for _, q := range gradleQuotedRe.FindAllStringSubmatch(m[1]+m[2], -1) {
					b.addModule(path.Join(b.dir, strings.ReplaceAll(strings.TrimPrefix(q[1], ":"), ":", "/")))
				}
			}
		case slices.Contains(gradleBuildFiles, name):
			b.addModule(dir)
		}
	}
	// With every module known, read what each references and which hold a
	// Dockerfile.
	for _, f := range files {
		dir := path.Dir(f)
		b := builds.of(dir)
		if b == nil {
			continue
		}
		switch name := path.Base(f); {
		case isDockerfile(name):
			b.deploy(b.module(dir))
		case slices.Contains(gradleBuildFiles, name) && b.module(dir) == dir:
			b.modules[dir] = b.references(text[f])
		}
	}
	for _, sn := range found {
		if len(sn.claims) == 0 {
			continue
		}
		if b := builds.of(sn.dir); b != nil {
			b.deploy(b.module(sn.dir))
		}
		if b, m := builds.claimed(sn.claims); b != nil {
			b.deploy(m)
		}
	}
	return builds
}

// claimed is the module claims name, by the rule attribute applies to units:
// for the first claim that names exactly one module of any build, that
// module.
func (bs gradleBuilds) claimed(claims []string) (*gradleBuild, string) {
	for _, c := range claims {
		var build *gradleBuild
		var named []string
		for _, b := range bs {
			for m := range b.modules {
				if sameName(path.Base(m), c) {
					build, named = b, append(named, m)
				}
			}
		}
		if len(named) == 1 {
			return build, named[0]
		}
	}
	return nil, ""
}

// of is the build dir belongs to: the nearest settings file at or above it.
func (bs gradleBuilds) of(dir string) *gradleBuild {
	for d := dir; ; d = path.Dir(d) {
		if b := bs[d]; b != nil || d == "." {
			return b
		}
	}
}

// absorbs reports whether f, a module's build file or a Dockerfile inside a
// Gradle build, makes no unit: the build's units are its deployables.
func (bs gradleBuilds) absorbs(f string) bool {
	b, name := bs.of(path.Dir(f)), path.Base(f)
	return b != nil && path.Dir(f) != b.dir && (slices.Contains(gradleBuildFiles, name) || isDockerfile(name))
}

func (b *gradleBuild) addModule(m string) {
	if _, ok := b.modules[m]; !ok && m != b.dir {
		b.modules[m] = nil
	}
}

func (b *gradleBuild) deploy(m string) {
	if m != "" && !testModule(m) {
		b.deployable[m] = true
	}
}

// testModule reports whether module m is named like a test, a word of its
// directory name being one of testFileWords (smoke-tests, integrationTest):
// the image such a module builds runs the tests, not the service.
func testModule(m string) bool {
	return slices.ContainsFunc(splitWords(path.Base(m)), func(w string) bool { return testFileWords[w] })
}

// module is the module holding dir, "" when none does.
func (b *gradleBuild) module(dir string) string {
	for d := dir; d != b.dir && d != "."; d = path.Dir(d) {
		if _, ok := b.modules[d]; ok {
			return d
		}
	}
	return ""
}

// references are the modules a build file names, each once. A reference is
// matched to a module by its words, so projects.fooBar is foo-bar.
func (b *gradleBuild) references(text string) []string {
	text = gradleTestDepRe.ReplaceAllString(gradleText(text), "")
	var refs [][]string
	for _, m := range gradleProjectRe.FindAllStringSubmatch(text, -1) {
		refs = append(refs, strings.FieldsFunc(m[1], func(r rune) bool { return r == ':' }))
	}
	for _, m := range gradleAccessorRe.FindAllStringSubmatch(text, -1) {
		refs = append(refs, strings.Split(m[1], "."))
	}
	var out []string
	for m := range b.modules {
		rel := strings.Split(strings.TrimPrefix(m, b.dir+"/"), "/")
		if slices.ContainsFunc(refs, func(ref []string) bool { return slices.EqualFunc(ref, rel, sameWords) }) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

func sameWords(a, b string) bool {
	return slices.Equal(splitWords(a), splitWords(b))
}

// gradleText is a Gradle file without its whole-line comments.
func gradleText(text string) string {
	return gradleCommentRe.ReplaceAllString(text, "")
}

// deployables are the build's deployed modules, sorted.
func (b *gradleBuild) deployables() []string {
	return sortedKeys(b.deployable)
}

// closure is the module m and every module it depends on, directly or not,
// short of another deployable, which is a unit of its own: m first, the rest
// sorted.
func (b *gradleBuild) closure(m string) []string {
	seen := map[string]bool{m: true}
	for next := []string{m}; len(next) > 0; {
		cur := next[len(next)-1]
		next = next[:len(next)-1]
		for _, d := range b.modules[cur] {
			if !seen[d] && !b.deployable[d] {
				seen[d] = true
				next = append(next, d)
			}
		}
	}
	delete(seen, m)
	return append([]string{m}, sortedKeys(seen)...)
}

// unitOf is the unit of dir in a build with several deployables, nearest
// being the nearest unit above it: a library module's is the one deployable
// depending on it, and the build's own when several do. The build's own
// directory, and any other that no module holds, is the build's too. sharedBy
// lists the deployables when the unit is the build.
func (b *gradleBuild) unitOf(dir, nearest string) (unit string, sharedBy []string) {
	if len(b.deployable) < 2 {
		return nearest, nil
	}
	m := b.module(dir)
	switch {
	case m == "" && nearest == b.dir:
		return b.dir, b.deployables()
	case m == "" || b.deployable[m]:
		return nearest, nil
	}
	switch sharedBy = b.owners(m); len(sharedBy) {
	case 0:
		return nearest, nil
	case 1:
		return sharedBy[0], nil
	}
	return b.dir, sharedBy
}

// owners are the deployables whose closure holds module m, sorted.
func (b *gradleBuild) owners(m string) []string {
	var out []string
	for _, d := range b.deployables() {
		if slices.Contains(b.closure(d), m) {
			out = append(out, d)
		}
	}
	return out
}

// gradleBuildAt is the Gradle build holding dir, read the way discovery
// reads it, or nil. With no settings file at or above dir it is nil without
// a walk. Nothing is deployed in it: the caller says what is.
func gradleBuildAt(fsys fs.FS, dir string) *gradleBuild {
	for d := dir; !slices.ContainsFunc(gradleSettingsFiles, func(f string) bool {
		_, err := fs.Stat(fsys, path.Join(d, f))
		return err == nil
	}); d = path.Dir(d) {
		if d == "." {
			return nil
		}
	}
	files, err := listSources(fsys, dir, func(f string) bool { return isGradleFile(path.Base(f)) })
	if err != nil {
		return nil
	}
	text := map[string]string{}
	for _, f := range files {
		if data, err := readRegular(fsys, f); err == nil {
			text[f] = string(data)
		}
	}
	return readGradleBuilds(files, text, nil).of(dir)
}
