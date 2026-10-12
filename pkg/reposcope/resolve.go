package reposcope

import (
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// foldPathCase reports whether entry paths compare case-insensitively: on
// the case-insensitive filesystems macOS and Windows ship with, a user who
// types "Services/Checkout" is standing in services/checkout. A variable, so
// both rules can be exercised on any host.
var foldPathCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// Status is the entry in effect for one directory, with the reason, as
// `repo-scope current` and `describe` print it. Linked is false and Entry nil
// when the directory is linked but no entry covers it: that is reported,
// never silent.
type Status struct {
	Linked      bool   `json:"linked" yaml:"linked"`
	File        string `json:"file,omitempty" yaml:"file,omitempty"`
	Environment string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Dir         string `json:"dir,omitempty" yaml:"dir,omitempty"`
	Entry       *Entry `json:"entry,omitempty" yaml:"entry,omitempty"`
	// Filters maps each data object to the expression the entry inserts.
	Filters map[string]string `json:"filters,omitempty" yaml:"filters,omitempty"`
	// Reason explains a nil Entry, or an entry chosen by name that does not
	// cover Dir.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// Warning is File.Warning: keys the entries apply without.
	Warning string   `json:"warning,omitempty" yaml:"warning,omitempty"`
	Others  []string `json:"others,omitempty" yaml:"others,omitempty"`
	Hosts   []string `json:"hosts,omitempty" yaml:"hosts,omitempty"`
}

// Resolve picks the entry for (host, relDir) from f, or the one named by
// name when it is not empty. A named entry applies wherever it is asked
// for; an unknown name is a *NotFoundError. Otherwise the entry whose Path or
// one of whose Paths covers relDir wins, the longest when several do, then
// the entry without either. A path covers the directories under it by whole
// segment: "services/checkout" does not cover "services/checkout-v2". With no
// entry, Entry is nil and Reason names the paths the entries do cover, or the
// two entries that cover relDir by the same path.
//
// Path comparison folds case on darwin and windows and is exact elsewhere.
func Resolve(f *File, host, relDir, name string) (*Status, error) {
	s := &Status{File: FileName, Environment: host, Dir: relDir, Warning: f.Warning(), Hosts: f.Hosts()}
	entries := f.Entries(host)
	switch {
	case name != "":
		s.Entry = entryNamed(entries, name)
		if s.Entry == nil {
			return nil, &NotFoundError{Name: name, Host: host, Known: f.Names(host)}
		}
		if paths := s.Entry.paths(); len(paths) > 0 && !slices.ContainsFunc(paths, func(p string) bool { return covers(p, relDir) }) {
			s.Reason = fmt.Sprintf("selected by name; it covers %s, not %q", strings.Join(paths, ", "), relDir)
		}
	case len(entries) == 0:
		s.Reason = fmt.Sprintf("no entry for %s; the file defines: %s", host, strings.Join(f.Hosts(), ", "))
		return s, nil
	default:
		var tied *Entry
		if s.Entry, tied = closestEntry(entries, relDir); tied != nil {
			s.Reason = fmt.Sprintf("covered by entries %s and %s; pick one with --repo-scope <name>", s.Entry.Name, tied.Name)
			s.Entry = nil
		}
	}
	if s.Entry == nil {
		if s.Reason == "" {
			s.Reason = fmt.Sprintf("no entry covers %q; entries here cover %s; pick one with --repo-scope <name>",
				relDir, strings.Join(entryPaths(entries), ", "))
		}
		s.Others = othersThan(entries, "")
		return s, nil
	}
	s.Linked = true
	s.Others = othersThan(entries, s.Entry.Name)
	s.Filters = map[string]string{}
	for object, filter := range Filters(s.Entry) {
		if filter.Expr != "" {
			s.Filters[object] = filter.Expr
		}
	}
	return s, nil
}

// closestEntry is the entry with the longest path covering relDir, else the
// one with no path. tied is another entry with a covering path as long: as
// both are relDir or its ancestors, the same directory.
func closestEntry(entries []Entry, relDir string) (best, tied *Entry) {
	var wide *Entry
	longest := 0
	for i := range entries {
		e := &entries[i]
		if len(e.paths()) == 0 {
			wide = e
		}
		for _, p := range e.paths() {
			if !covers(p, relDir) || len(p) < longest {
				continue
			}
			switch {
			case len(p) > longest:
				best, tied, longest = e, nil, len(p)
			case e != best:
				tied = e
			}
		}
	}
	if best == nil {
		return wide, nil
	}
	return best, tied
}

// paths are the directories e covers, none for the whole repository.
func (e *Entry) paths() []string {
	if len(e.Paths) > 0 {
		return e.Paths
	}
	if e.Path != "" {
		return []string{e.Path}
	}
	return nil
}

// covers reports whether the entry path p is relDir or one of its ancestors,
// comparing whole path segments.
func covers(p, relDir string) bool {
	if foldPathCase {
		p, relDir = strings.ToLower(p), strings.ToLower(relDir)
	}
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
		out = append(out, e.paths()...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}
