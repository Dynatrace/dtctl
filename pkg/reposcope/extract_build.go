package reposcope

import (
	"encoding/json"
	"path"
	"regexp"
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

// interpreters are the programs a Dockerfile runs something else with; the
// name worth reading is that something else.
var interpreters = keySet("sh", "bash", "exec", "java", "node", "python", "python3",
	"dotnet", "npm", "yarn", "pnpm", "ruby", "php", "tini", "dumb-init")

// dockerEntrypoint reads what the image runs. Docker appends CMD to
// ENTRYPOINT, so the last of each are read together; a jar, war or dll
// argument wins, since it names the artifact, and otherwise the first
// argument that is neither an interpreter nor a flag.
func dockerEntrypoint(text, file string) (token, bool) {
	var entrypoint, cmd []string
	for _, m := range dockerRunRe.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[1], "ENTRYPOINT") {
			entrypoint = m
		} else {
			cmd = m
		}
	}
	var args []string
	line := ""
	for _, m := range [][]string{cmd, entrypoint} {
		if m != nil {
			args = append(splitDockerArgs(m[2]), args...)
			line = m[0]
		}
	}
	name := runTarget(args)
	if name == "" {
		return token{}, false
	}
	return tokenAt(kindEntrypoint, name, file, text, line)
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
// extension.
func runTarget(args []string) string {
	for _, a := range args {
		switch path.Ext(a) {
		case ".jar", ".war", ".dll":
			return strings.TrimSuffix(path.Base(a), path.Ext(a))
		}
	}
	for _, a := range args {
		base := path.Base(a)
		if strings.HasPrefix(a, "-") || interpreters[base] {
			continue
		}
		return strings.TrimSuffix(base, path.Ext(base))
	}
	return ""
}
