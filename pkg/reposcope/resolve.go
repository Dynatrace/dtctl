package reposcope

import (
	"fmt"
	"io/fs"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// foldPathCase reports whether entry paths compare case-insensitively: on
// the case-insensitive filesystems macOS and Windows ship with, a user who
// types "Services/Checkout" is standing in services/checkout. A variable, so
// both rules can be exercised on any host.
var foldPathCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// StatusCode says which answer a Status is, for a program; Reason says it
// for a person.
type StatusCode string

const (
	// StatusLinked: Entry is the entry asked for, and Selected says how it
	// was chosen.
	StatusLinked StatusCode = "linked"
	// StatusNoEntry: the environment has entries and none covers Dir.
	StatusNoEntry StatusCode = "no_entry"
	// StatusAmbiguousEntry: the entries in Tied cover Dir as closely, so
	// none applies.
	StatusAmbiguousEntry StatusCode = "ambiguous_entry"
	// StatusNoEntryForEnvironment: the file has no entry for Environment.
	StatusNoEntryForEnvironment StatusCode = "no_entry_for_environment"

	// Resolve returns the codes above. The ones below are for the caller,
	// which is who knows that there was nothing to resolve.

	// StatusNoFile: the repository has no scope file (Location.Exists).
	StatusNoFile StatusCode = "no_file"
	// StatusInvalidFile: the scope file is there and does not load.
	StatusInvalidFile StatusCode = "invalid_file"
	// StatusNotARepository: the directory is in no git repository.
	StatusNotARepository StatusCode = "not_a_repository"
)

// Selection says how the entry of a linked Status was chosen.
type Selection string

const (
	// SelectedByDirectory: the entry covers Dir, by its path or as the entry
	// of the whole repository. It is the entry in effect there.
	SelectedByDirectory Selection = "directory"
	// SelectedByBuild: the entry covers Dir through the Gradle build, and is
	// the entry in effect there.
	SelectedByBuild Selection = "build"
	// SelectedByName: the caller named the entry. It need not be the entry
	// in effect in Dir, nor cover it at all.
	SelectedByName Selection = "name"
)

// Status is the entry in effect for one directory, with the reason, as
// `repo-scope current` and `describe` print it. Code says which answer it
// is. Linked is false and Entry nil whenever no entry was selected, and a
// directory of a linked repository that no entry covers is such an answer:
// it is reported, never silent.
type Status struct {
	Linked      bool       `json:"linked" yaml:"linked"`
	Code        StatusCode `json:"code" yaml:"code"`
	File        string     `json:"file,omitempty" yaml:"file,omitempty"`
	Environment string     `json:"environment,omitempty" yaml:"environment,omitempty"`
	Dir         string     `json:"dir,omitempty" yaml:"dir,omitempty"`
	Entry       *Entry     `json:"entry,omitempty" yaml:"entry,omitempty"`
	// Selected says how Entry was chosen; it is empty without one.
	Selected Selection `json:"selected,omitempty" yaml:"selected,omitempty"`
	// Filters maps each data object to the expression the entry inserts.
	Filters map[string]string `json:"filters,omitempty" yaml:"filters,omitempty"`
	// LiveDebuggerFilters are the Live Debugger workspace filter sets the
	// entry renders to; see LiveDebuggerFilters. It is the one rendering:
	// describe and the breakpoint commands read it through
	// LiveDebuggerFilterSets.
	LiveDebuggerFilters []map[string][]string `json:"liveDebuggerFilters,omitempty" yaml:"liveDebuggerFilters,omitempty"`
	// liveDebuggerReason says why an entry renders no filter sets.
	liveDebuggerReason string
	// Reason explains a nil Entry, an entry chosen by name that does not
	// cover Dir, or one that covers it through a Gradle build. It is for a
	// person; a program reads Code, Selected and Tied.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// Tied names the entries that cover Dir as closely, in file order.
	Tied []string `json:"tied,omitempty" yaml:"tied,omitempty"`
	// Warning is File.Warning: keys the entries apply without.
	Warning string   `json:"warning,omitempty" yaml:"warning,omitempty"`
	Others  []string `json:"others,omitempty" yaml:"others,omitempty"`
	Hosts   []string `json:"hosts,omitempty" yaml:"hosts,omitempty"`
}

// Resolve picks the entry for (host, relDir) from f, or the one named by
// name when it is not empty. A named entry applies wherever it is asked
// for; an unknown name is a *NotFoundError. Otherwise the entry whose Path
// covers relDir wins, the longest when several do, then the entry without
// one. A path covers the directories under it by whole segment:
// "services/checkout" does not cover "services/checkout-v2". Inside a Gradle
// build, read from repo, an entry whose Path is a module also covers the
// modules it depends on, as closely as relDir's own module. With no entry,
// Entry is nil and Reason names the paths the entries do cover, or the
// entries that cover relDir as closely (see Ambiguous).
//
// relDir is Location.RelDir: a clean, slash-separated path relative to the
// repository root, "." at the root. Anything else is an *InvalidError. repo
// may be nil for a caller with no repository to read; no build is read then.
//
// Path comparison folds case on darwin and windows and is exact elsewhere.
// The Status holds a copy of the entry, so it stays the answer it was when f
// changes.
func Resolve(repo fs.FS, f *File, host, relDir, name string) (*Status, error) {
	if !fs.ValidPath(relDir) {
		return nil, &InvalidError{Source: fmt.Sprintf("directory %q", relDir), Msg: "is not a clean path relative to the repository root"}
	}
	s := &Status{File: FileName, Environment: host, Dir: relDir, Warning: f.Warning(), Hosts: f.Hosts()}
	entries := f.Environments[host]
	// Reading the build walks the repository, and Resolve runs on every
	// scoped query: it is read when its answer is first needed, if at all.
	build := sync.OnceValue(func() gradleCoverage { return newGradleCoverage(repo, entries, relDir) })
	var entry *Entry
	switch {
	case name != "":
		entry = entryNamed(entries, name)
		if entry == nil {
			return nil, &NotFoundError{Name: name, Host: host, Known: f.Names(host)}
		}
	case len(entries) == 0:
		s.Code = StatusNoEntryForEnvironment
		s.Reason = fmt.Sprintf("no entry for %s; the file defines no entries", host)
		if hosts := f.Hosts(); len(hosts) > 0 {
			s.Reason = fmt.Sprintf("no entry for %s; the file defines: %s", host, strings.Join(hosts, ", "))
		}
		return s, nil
	default:
		var tied []*Entry
		// Said whatever is selected instead: an entry that covers the
		// directory by its path, the repository's, or none.
		s.Reason = build().partial
		if entry, tied = closestEntry(entries, relDir, build()); tied != nil {
			s.Tied = []string{entry.Name}
			for _, e := range tied {
				s.Tied = append(s.Tied, e.Name)
			}
			s.Code = StatusAmbiguousEntry
			s.Reason = "covered by entries " + joinAnd(s.Tied)
			entry = nil
		}
	}
	if entry == nil {
		if s.Code == "" {
			s.Code = StatusNoEntry
			s.Reason = fmt.Sprintf("no entry covers %q; entries here cover %s", relDir, strings.Join(entryPaths(entries), ", "))
			if partial := build().partial; partial != "" {
				s.Reason += "; " + partial
			}
		}
		s.Others = othersThan(entries, "")
		return s, nil
	}
	s.Selected = SelectedByDirectory
	if name != "" {
		s.Selected = SelectedByName
	}
	if entry.Path != "" && !covers(entry.Path, relDir) {
		if build := build(); build.owns(entry) {
			if name == "" {
				s.Selected = SelectedByBuild
			}
			// The module is a directory's name, which is whatever a clone
			// carries: quoted, like every path a reason names.
			s.Reason = fmt.Sprintf("covered through the Gradle build: %q depends on %q", entry.Path, build.module)
		} else {
			s.Reason = fmt.Sprintf("selected by name; its path %q does not cover %q", entry.Path, relDir)
		}
	}
	e := entry.clone()
	s.Entry = &e
	s.Code = StatusLinked
	s.Linked = true
	s.Others = othersThan(entries, s.Entry.Name)
	s.Filters = map[string]string{}
	for object, filter := range Filters(s.Entry) {
		if filter.Expr != "" {
			s.Filters[object] = filter.Expr
		}
	}
	s.LiveDebuggerFilters, _, s.liveDebuggerReason = LiveDebuggerFilters(s.Entry)
	return s, nil
}

// LiveDebuggerFilterSets is the entry's LiveDebuggerFilters, or, when it
// renders none, ok false and the reason. A Status without an entry renders
// none either.
func (s *Status) LiveDebuggerFilterSets() (sets []map[string][]string, ok bool, reason string) {
	switch {
	case len(s.LiveDebuggerFilters) > 0:
		return s.LiveDebuggerFilters, true, ""
	case s.Entry == nil:
		return nil, false, s.Reason
	}
	return nil, false, s.liveDebuggerReason
}

// Ambiguous reports whether several entries cover Dir as closely, none
// applying; Tied names them.
func (s *Status) Ambiguous() bool { return len(s.Tied) > 0 }

// depth is how many segments the path p has, 0 for none. It is how close a
// path that covers a directory is to it: every such path is a prefix of the
// directory by whole segments, so the deeper one is the closer. Its length in
// bytes is not: the same path is longer in another case or composition.
func depth(p string) int {
	if p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// closestEntry is the entry with the deepest path covering relDir, else the
// one with no path. An entry the build covers relDir through is as close as
// relDir's module, whether or not its own path covers relDir from further
// up: a module inside one deployable's directory that another deployable
// depends on as well belongs to both. tied are the other entries as close, in
// file order: further deployables whose closure holds that module.
func closestEntry(entries []Entry, relDir string, build gradleCoverage) (best *Entry, tied []*Entry) {
	var wide *Entry
	deepest := 0
	for i := range entries {
		e := &entries[i]
		n := 0
		if e.Path == "" {
			wide = e
			continue
		}
		if covers(e.Path, relDir) {
			n = depth(e.Path)
		}
		if build.owns(e) {
			n = max(n, depth(build.module))
		}
		switch {
		case n == 0 || n < deepest:
			continue
		case n > deepest:
			best, tied, deepest = e, nil, n
		default:
			tied = append(tied, e)
		}
	}
	if best == nil {
		return wide, nil
	}
	return best, tied
}

// joinAnd lists words as prose: "a", "a and b", "a, b and c".
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// gradleCoverage is how the Gradle build holding a directory covers it: the
// module holding the directory, and the deployables whose closure holds that
// module. The deployables are the build's modules an entry's Path names:
// discovery infers them from Dockerfiles and manifests, but once an entry
// exists the scope file says which they are.
type gradleCoverage struct {
	module string
	owners []string
	// partial says that the build was not read whole, and why: it then
	// covers nothing, since a deployable that was not read may hold the
	// module as well. It names the build's directory.
	partial string
}

func newGradleCoverage(repo fs.FS, entries []Entry, relDir string) gradleCoverage {
	var c gradleCoverage
	if repo == nil || !slices.ContainsFunc(entries, func(e Entry) bool { return e.Path != "" }) {
		return c
	}
	b := gradleBuildAt(repo, relDir)
	if b == nil {
		return c
	}
	if b.partial != "" {
		c.partial = fmt.Sprintf("the Gradle build at %q was not read whole (%s), so no entry is selected through it", b.dir, b.partial)
		return c
	}
	// Sorted: two module directories that differ only in case cover relDir
	// equally where case is folded, and the answer must not depend on the
	// order a map is walked in.
	for _, m := range slices.Sorted(maps.Keys(b.modules)) {
		// b.module(relDir), folding case as covers does.
		if covers(m, relDir) && depth(m) > depth(c.module) {
			c.module = m
		}
		if slices.ContainsFunc(entries, func(e Entry) bool { return samePath(e.Path, m) }) {
			b.deployable[m] = true
		}
	}
	// Two modules whose directories differ only in case are one directory
	// where case is folded, and a build file names either: the directory
	// belongs to the deployables that depend on any of them.
	for _, m := range slices.Sorted(maps.Keys(b.modules)) {
		if c.module == "" || !samePath(m, c.module) {
			continue
		}
		for _, owner := range b.owners(m) {
			if !slices.Contains(c.owners, owner) {
				c.owners = append(c.owners, owner)
			}
		}
	}
	return c
}

// owns reports whether e's path is a deployable whose closure holds the
// module.
func (c gradleCoverage) owns(e *Entry) bool {
	return slices.ContainsFunc(c.owners, func(o string) bool { return samePath(e.Path, o) })
}

// foldPath is the form paths are compared in, by check, covers and
// samePath alike. Unicode composition is folded on every OS: a file holds
// the composed spelling (see validatePath), while macOS hands a program
// whichever one the directory was created or typed with. Case is folded
// when foldCase is set, letter by letter the way strings.EqualFold does, so
// no letter is taken for two as a full case folding would take "ß".
func foldPath(p string, foldCase bool) string {
	p = norm.NFC.String(p)
	if !foldCase {
		return p
	}
	return strings.Map(func(r rune) rune {
		folded := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			folded = min(folded, f)
		}
		return folded
	}, p)
}

// samePath reports whether two paths name one directory, folding as covers
// does.
func samePath(a, b string) bool {
	return foldPath(a, foldPathCase) == foldPath(b, foldPathCase)
}

// covers reports whether the entry path p is relDir or one of its ancestors,
// comparing whole path segments.
func covers(p, relDir string) bool {
	p, relDir = foldPath(p, foldPathCase), foldPath(relDir, foldPathCase)
	return relDir == p || strings.HasPrefix(relDir, p+"/")
}

func entryNamed(entries []Entry, name string) *Entry {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}

func othersThan(entries []Entry, name string) []string {
	var out []string
	for _, e := range entries {
		if e.Name != name {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

func entryPaths(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Path != "" {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out
}
