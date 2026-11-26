package reposcope

import (
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Build files name the artifact a unit produces; that name is often what the
// service is called at runtime, and always what the people who own it call it.

var (
	goModuleRe     = regexp.MustCompile(`(?m)^module[ \t]+"?([^\s"]+)"?`)
	majorVersionRe = regexp.MustCompile(`/v[0-9]+$`)
	pomParentRe    = regexp.MustCompile(`(?s)<parent>.*?</parent>`)
	pomArtifactRe  = regexp.MustCompile(`<artifactId>\s*([^<]+?)\s*</artifactId>`)
	assemblyNameRe = regexp.MustCompile(`<AssemblyName>\s*([^<]+?)\s*</AssemblyName>`)
	tomlSectionRe  = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	tomlNameRe     = regexp.MustCompile(`^\s*name\s*=\s*["']([^"']+)["']`)
	dockerRunRe    = regexp.MustCompile(`(?im)^[ \t]*(ENTRYPOINT|CMD)[ \t]+(.+)$`)

	// The Gradle expressions read one assignment in either DSL: `name = "x"`
	// and `name = 'x'`, and Kotlin's `name.set("x")` for a property. The
	// archives name is matched anywhere on a line not commented out before
	// it, so a one-line block such as `base { archivesName.set("x") }` is
	// read too.
	gradleRootProjectRe  = regexp.MustCompile(`(?m)^[ \t]*rootProject\.name[ \t]*=[ \t]*["']([^"']+)["']`)
	gradleArchivesNameRe = regexp.MustCompile(`(?m)^[^/\n]*?\b(?:archivesBaseName|archivesName)[ \t]*(?:=|\.set\()[ \t]*["']([^"']+)["']`)
)

// goModule reads the last segment of go.mod's module path, without a /vN
// major-version suffix.
func goModule(text, file string) (token, bool) {
	m := goModuleRe.FindStringSubmatch(text)
	if m == nil {
		return token{}, false
	}
	return tokenAt(kindModule, majorVersionRe.ReplaceAllString(m[1], ""), file, text, m[0])
}

// packageName reads package.json's name; an npm scope ("@acme/") is dropped
// with the rest of the path.
func packageName(text, file string) (token, bool) {
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(text), &pkg) != nil || pkg.Name == "" {
		return token{}, false
	}
	return tokenAt(kindPackage, pkg.Name, file, text, `"name"`)
}

// pomArtifact reads the project's own artifactId: the first one outside the
// <parent> block, which names the parent POM instead.
func pomArtifact(text, file string) (token, bool) {
	// Blank the parent out with as many newlines as it spans, so the line
	// numbers after it stay true.
	own := pomParentRe.ReplaceAllStringFunc(text, func(s string) string {
		return strings.Repeat("\n", strings.Count(s, "\n"))
	})
	m := pomArtifactRe.FindStringSubmatch(own)
	if m == nil {
		return token{}, false
	}
	return tokenAt(kindArtifact, m[1], file, own, m[0])
}

// gradleRootProject reads the build's name from a Gradle settings file.
func gradleRootProject(text, file string) (token, bool) {
	if m := gradleRootProjectRe.FindStringSubmatch(text); m != nil {
		return tokenAt(kindProject, m[1], file, text, m[0])
	}
	return token{}, false
}

// gradleArchivesName reads the name a Gradle module's archives are given in
// place of the module's directory name; most modules set none.
func gradleArchivesName(text, file string) (token, bool) {
	if m := gradleArchivesNameRe.FindStringSubmatch(text); m != nil {
		return tokenAt(kindArtifact, m[1], file, text, m[0])
	}
	return token{}, false
}

// pyprojectName reads `name = "…"` from [project] or [tool.poetry]; no TOML
// parser is needed for one key.
func pyprojectName(text, file string) (token, bool) {
	section := ""
	for i, line := range strings.Split(text, "\n") {
		if m := tomlSectionRe.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		if m := tomlNameRe.FindStringSubmatch(line); m != nil && (section == "project" || section == "tool.poetry") {
			return newToken(kindProject, m[1], file, i+1)
		}
	}
	return token{}, false
}

// assemblyName reads a .csproj's AssemblyName, which defaults to the project
// file's stem.
func assemblyName(text, file string) (token, bool) {
	if m := assemblyNameRe.FindStringSubmatch(text); m != nil {
		return tokenAt(kindAssembly, m[1], file, text, m[0])
	}
	return newToken(kindAssembly, strings.TrimSuffix(path.Base(file), ".csproj"), file, 0)
}

// launchers are the programs and scripts a Dockerfile starts something else
// with, compared without their extension: an interpreter or a process
// supervisor, a server that is configured rather than built (nginx, uvicorn,
// gunicorn), and the entry files every project calls the same (index.js,
// run.sh, npm start, wsgi.py). None of them names the service; the name worth
// reading is what comes after.
var launchers = keySet("sh", "bash", "exec", "java", "node", "python", "python3",
	"dotnet", "npm", "yarn", "pnpm", "ruby", "php", "tini", "dumb-init",
	"nginx", "uvicorn", "gunicorn", "index", "run", "start", "wsgi", "asgi")

var dockerFromRe = regexp.MustCompile(`(?im)^[ \t]*FROM[ \t]`)

// dockerEntrypoint reads what the image runs. The image is the last stage of
// the file, so only that stage is read: an earlier one runs a build tool.
// Docker appends CMD to ENTRYPOINT, so the last of each are read together; a
// jar, war or dll argument wins, since it names the artifact, and otherwise
// the first argument that is neither a launcher nor a flag.
func dockerEntrypoint(text, file string) (token, bool) {
	stage := 0
	if from := dockerFromRe.FindAllStringIndex(text, -1); len(from) > 0 {
		stage = from[len(from)-1][0]
	}
	var entrypoint, cmd []int
	for _, m := range dockerRunRe.FindAllStringSubmatchIndex(text[stage:], -1) {
		if strings.EqualFold(text[stage+m[2]:stage+m[3]], "ENTRYPOINT") {
			entrypoint = m
		} else {
			cmd = m
		}
	}
	var args []string
	line := 0
	for _, m := range [][]int{cmd, entrypoint} {
		if m != nil {
			args = append(splitDockerArgs(text[stage+m[4]:stage+m[5]]), args...)
			line = strings.Count(text[:stage+m[0]], "\n") + 1
		}
	}
	name := runTarget(args)
	if name == "" {
		return token{}, false
	}
	return newToken(kindEntrypoint, name, file, line)
}

// splitDockerArgs splits ENTRYPOINT/CMD arguments in exec form (a JSON
// array) or shell form.
func splitDockerArgs(args string) []string {
	var exec []string
	if json.Unmarshal([]byte(strings.TrimSpace(args)), &exec) == nil {
		return exec
	}
	return strings.Fields(args)
}

// runTarget picks the name a command line runs, without directory or
// extension. An argument is never a name when it is a flag, an assignment
// (KEY=value, --key=value) or a line of its own (the script of sh -c): those
// carry configuration, credentials among it, and a flag may hold a jar that
// is an agent rather than the artifact.
func runTarget(args []string) string {
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool {
		return strings.HasPrefix(a, "-") || strings.ContainsAny(a, "= \t\r\n")
	})
	for _, a := range args {
		switch path.Ext(a) {
		case ".jar", ".war", ".dll":
			return strings.TrimSuffix(path.Base(a), path.Ext(a))
		}
	}
	for _, a := range args {
		if stem := strings.TrimSuffix(path.Base(a), path.Ext(a)); !launchers[stem] {
			return stem
		}
	}
	return ""
}
