package reposcope

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"io/fs"
	"regexp"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// monorepoFile is three path-bound entries, as `set … --path` writes them.
func monorepoFile() *File {
	return &File{Environments: map[string][]Entry{prodHost: {
		{Name: "checkout", Path: "services/checkout", ServiceNames: []string{"checkout"}},
		{Name: "checkout-v2", Path: "services/checkout-v2", ServiceNames: []string{"checkout-v2"}},
		{Name: "ledger", Path: "services/ledger", ServiceNames: []string{"ledger"}},
		{Name: "ledger-close", Path: "services/ledger/close", ServiceNames: []string{"ledger-close"}},
	}}}
}

// gradleFile is an entry for each of the gradle-multi fixture's two
// deployables, which share the webproxy-core module.
func gradleFile() *File {
	return &File{Environments: map[string][]Entry{prodHost: {
		{Name: "webproxy", Path: "webproxy", ServiceNames: []string{"webproxy"}},
		{Name: "admin", Path: "admin", ServiceNames: []string{"admin"}},
	}}}
}

func withRepositoryWide(f *File) *File {
	f.Upsert(prodHost, Entry{Name: "shop", ServiceNames: []string{"shop"}})
	return f
}

func TestResolve(t *testing.T) {
	gradle := fixture(t, "gradle-multi")
	onlyWebproxy := gradleFile()
	onlyWebproxy.Remove(prodHost, "admin")
	// A build file that is a symlink is not read: webproxy references nothing.
	symlinked := fixture(t, "gradle-multi")
	symlinked["webproxy/build.gradle.kts"] = &fstest.MapFile{Data: []byte("../admin/build.gradle.kts"), Mode: fs.ModeSymlink}
	tests := []struct {
		name      string
		repo      fs.FS
		file      *File
		host      string
		dir       string
		pick      string
		entry     string // "" for no entry
		reason    string
		others    []string
		ambiguous bool
		code      StatusCode
		selected  Selection
	}{
		{code: StatusLinked, selected: SelectedByDirectory, name: "exact path", file: monorepoFile(), host: prodHost, dir: "services/checkout",
			entry: "checkout", others: []string{"checkout-v2", "ledger", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByDirectory, name: "below a path", file: monorepoFile(), host: prodHost, dir: "services/checkout/internal/api",
			entry: "checkout", others: []string{"checkout-v2", "ledger", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByDirectory, name: "a prefix that is not a path segment", file: monorepoFile(), host: prodHost, dir: "services/checkout-v2/cmd",
			entry: "checkout-v2", others: []string{"checkout", "ledger", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByDirectory, name: "the longest path wins", file: monorepoFile(), host: prodHost, dir: "services/ledger/close/job",
			entry: "ledger-close", others: []string{"checkout", "checkout-v2", "ledger"}},
		{code: StatusNoEntry, name: "no entry covers the directory", file: monorepoFile(), host: prodHost, dir: ".",
			reason: `no entry covers "."; entries here cover services/checkout, services/checkout-v2, services/ledger, services/ledger/close`,
			others: []string{"checkout", "checkout-v2", "ledger", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByDirectory, name: "the repository-wide entry catches the rest", file: withRepositoryWide(monorepoFile()), host: prodHost, dir: "docs",
			entry: "shop", others: []string{"checkout", "checkout-v2", "ledger", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByDirectory, name: "a path entry beats the repository-wide one", file: withRepositoryWide(monorepoFile()), host: prodHost, dir: "services/ledger",
			entry: "ledger", others: []string{"checkout", "checkout-v2", "ledger-close", "shop"}},
		{code: StatusNoEntryForEnvironment, name: "no entries for this host", file: monorepoFile(), host: stgHost, dir: "services/checkout",
			reason: "no entry for stg98765.apps.dynatrace.com; the file defines: abc12345.apps.dynatrace.com"},
		{code: StatusNoEntryForEnvironment, name: "a file with no entries at all", file: &File{}, host: prodHost, dir: ".",
			reason: "no entry for abc12345.apps.dynatrace.com; the file defines no entries"},
		{code: StatusLinked, selected: SelectedByName, name: "selected by name from anywhere", file: monorepoFile(), host: prodHost, dir: ".", pick: "ledger",
			entry: "ledger", reason: `selected by name; its path "services/ledger" does not cover "."`,
			others: []string{"checkout", "checkout-v2", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByName, name: "selected by name where it applies anyway", file: monorepoFile(), host: prodHost, dir: "services/ledger", pick: "ledger",
			entry: "ledger", others: []string{"checkout", "checkout-v2", "ledger-close"}},
		{code: StatusLinked, selected: SelectedByDirectory, name: "a Gradle deployable", repo: gradle, file: gradleFile(), host: prodHost, dir: "webproxy/src",
			entry: "webproxy", others: []string{"admin"}},
		{code: StatusLinked, selected: SelectedByBuild, name: "a library one deployable depends on", repo: gradle, file: gradleFile(), host: prodHost, dir: "webproxy-api/src",
			entry: "webproxy", reason: "covered through the Gradle build: \"webproxy\" depends on \"webproxy-api\"", others: []string{"admin"}},
		{code: StatusAmbiguousEntry, name: "a library two deployables depend on", repo: gradle, file: gradleFile(), host: prodHost, dir: "webproxy-core/src/main/kotlin",
			reason: "covered by entries webproxy and admin", others: []string{"admin", "webproxy"}, ambiguous: true},
		{code: StatusLinked, selected: SelectedByName, name: "a library two deployables depend on, selected by name", repo: gradle, file: gradleFile(), host: prodHost, dir: "webproxy-core",
			pick: "admin", entry: "admin", reason: "covered through the Gradle build: \"admin\" depends on \"webproxy-core\"", others: []string{"webproxy"}},
		{code: StatusLinked, selected: SelectedByBuild, name: "a library only an entry's deployable is known to depend on", repo: gradle, file: onlyWebproxy, host: prodHost, dir: "webproxy-core",
			entry: "webproxy", reason: "covered through the Gradle build: \"webproxy\" depends on \"webproxy-core\""},
		{code: StatusNoEntry, name: "the settings directory", repo: gradle, file: gradleFile(), host: prodHost, dir: ".",
			reason: `no entry covers "."; entries here cover admin, webproxy`, others: []string{"admin", "webproxy"}},
		{code: StatusLinked, selected: SelectedByName, name: "selected by name outside its build closure", repo: gradle, file: gradleFile(), host: prodHost, dir: "admin", pick: "webproxy",
			entry: "webproxy", reason: `selected by name; its path "webproxy" does not cover "admin"`, others: []string{"admin"}},
		{code: StatusNoEntry, name: "a build file that does not read", repo: symlinked, file: gradleFile(), host: prodHost, dir: "webproxy-api",
			reason: `no entry covers "webproxy-api"; entries here cover admin, webproxy`, others: []string{"admin", "webproxy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := tt.repo
			if repo == nil {
				repo = fstest.MapFS{}
			}
			r, err := Resolve(repo, tt.file, tt.host, tt.dir, tt.pick)
			require.NoError(t, err)
			assert.Equal(t, tt.entry != "", r.Linked)
			assert.Equal(t, FileName, r.File)
			assert.Equal(t, tt.host, r.Environment)
			assert.Equal(t, tt.dir, r.Dir)
			if tt.entry == "" {
				assert.Nil(t, r.Entry)
			} else {
				require.NotNil(t, r.Entry)
				assert.Equal(t, tt.entry, r.Entry.Name)
			}
			assert.Equal(t, tt.reason, r.Reason)
			assert.Equal(t, tt.others, r.Others)
			assert.Equal(t, tt.ambiguous, r.Ambiguous())
			assert.Equal(t, tt.code, r.Code)
			assert.Equal(t, tt.selected, r.Selected)
		})
	}
}

// A tie's reason names every entry in it, in file order.
func TestResolve_Ambiguous(t *testing.T) {
	three := fixture(t, "gradle-multi")
	three["settings.gradle.kts"] = &fstest.MapFile{Data: []byte(`include(":webproxy", ":admin", ":edge", ":webproxy-api", ":webproxy-core")`)}
	three["edge/build.gradle.kts"] = &fstest.MapFile{Data: []byte(`dependencies { implementation(project(":webproxy-core")) }`)}
	f := gradleFile()
	f.Upsert(prodHost, Entry{Name: "edge", Path: "edge", ServiceNames: []string{"edge"}})
	r, err := Resolve(three, f, prodHost, "webproxy-core", "")
	require.NoError(t, err)
	assert.True(t, r.Ambiguous())
	assert.Nil(t, r.Entry)
	assert.Equal(t, "covered by entries webproxy, admin and edge", r.Reason)
}

// What a Status says, it says as data: a program that keeps one, or reads
// it from another program's output, finds the tie and the kind of answer in
// it without reading the reason.
func TestResolve_StatusSurvivesJSON(t *testing.T) {
	r, err := Resolve(fixture(t, "gradle-multi"), gradleFile(), prodHost, "webproxy-core", "")
	require.NoError(t, err)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	assert.JSONEq(t, `{"linked":false,"code":"ambiguous_entry","file":".dtctl-repo-scope.yaml","environment":"`+prodHost+`","dir":"webproxy-core",
		"reason":"covered by entries webproxy and admin","tied":["webproxy","admin"],"others":["admin","webproxy"],"hosts":["`+prodHost+`"]}`, string(data))
	var back Status
	require.NoError(t, json.Unmarshal(data, &back))
	assert.True(t, back.Ambiguous())
	assert.Equal(t, []string{"webproxy", "admin"}, back.Tied)

	r, err = Resolve(fixture(t, "gradle-multi"), gradleFile(), prodHost, "webproxy-api", "")
	require.NoError(t, err)
	data, err = json.Marshal(r)
	require.NoError(t, err)
	back = Status{}
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, StatusLinked, back.Code)
	assert.Equal(t, SelectedByBuild, back.Selected)
	assert.False(t, back.Ambiguous())
}

// What the package says is read by programs other than dtctl, and how a
// command line spells an option is the command's to say: no message built
// here names a flag.
func TestMessagesNameNoFlag(t *testing.T) {
	flag := regexp.MustCompile(`(^|[^-])--[a-z]`)
	for _, file := range []string{"schema.go", "decode.go", "resolve.go", "store.go", "errors.go"} {
		f, err := parser.ParseFile(gotoken.NewFileSet(), file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == gotoken.STRING {
				assert.NotRegexp(t, flag, lit.Value, file)
			}
			return true
		})
	}
}

func TestResolve_UnknownName(t *testing.T) {
	_, err := Resolve(fstest.MapFS{}, monorepoFile(), prodHost, ".", "ledgr")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, &NotFoundError{Name: "ledgr", Host: prodHost, Known: []string{"checkout", "checkout-v2", "ledger", "ledger-close"}}, notFound)
	assert.Equal(t, `repo scope "ledgr" not found for abc12345.apps.dynatrace.com (entries: checkout, checkout-v2, ledger, ledger-close)`, err.Error())

	_, err = Resolve(fstest.MapFS{}, monorepoFile(), stgHost, ".", "checkout")
	require.ErrorAs(t, err, &notFound)
	assert.Empty(t, notFound.Known)
	assert.Equal(t, `repo scope "checkout" not found: .dtctl-repo-scope.yaml defines no entry for stg98765.apps.dynatrace.com`, err.Error())
}

func TestResolve_PathCase(t *testing.T) {
	prev := foldPathCase
	t.Cleanup(func() { foldPathCase = prev })

	foldPathCase = true
	r, err := Resolve(fstest.MapFS{}, monorepoFile(), prodHost, "Services/Checkout/api", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry, "a case-insensitive filesystem folds case")
	assert.Equal(t, "checkout", r.Entry.Name)
	f := gradleFile()
	f.Environments[prodHost][0].Path = "WebProxy"
	r, err = Resolve(fixture(t, "gradle-multi"), f, prodHost, "WebProxy-API/src", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry, "and so does the module a Gradle build covers a directory through")
	assert.Equal(t, "webproxy", r.Entry.Name)

	foldPathCase = false
	r, err = Resolve(fstest.MapFS{}, monorepoFile(), prodHost, "Services/Checkout/api", "")
	require.NoError(t, err)
	assert.Nil(t, r.Entry, "a case-sensitive one does not")
}

// A directory is found under its path however the name was typed: macOS
// hands a program the decomposed spelling of a name its user sees as one
// with the composed one, and the file holds the composed one.
func TestResolve_PathUnicode(t *testing.T) {
	prev := foldPathCase
	t.Cleanup(func() { foldPathCase = prev })
	f := &File{Environments: map[string][]Entry{prodHost: {
		{Name: "cafe", Path: "services/caf\u00e9", ServiceNames: []string{"cafe"}},
		{Name: "long", Path: "\u017fvc", ServiceNames: []string{"long"}},
	}}}
	require.Nil(t, f.check())
	for _, fold := range []bool{true, false} {
		foldPathCase = fold
		r, err := Resolve(fstest.MapFS{}, f, prodHost, "services/cafe\u0301/api", "")
		require.NoError(t, err)
		require.NotNil(t, r.Entry, "fold case: %v", fold)
		assert.Equal(t, "cafe", r.Entry.Name)
		assert.Empty(t, r.Reason)
	}

	// One folding for every comparison: two paths that are the same
	// directory are also each other's cover.
	foldPathCase = true
	for _, pair := range [][2]string{{"\u017fvc", "svc"}, {"SVC", "svc"}, {"Stra\u00dfe", "STRASSE"}, {"caf\u00e9", "cafe\u0301"}, {"a", "b"}} {
		assert.Equal(t, samePath(pair[0], pair[1]), covers(pair[0], pair[1]), "%q", pair)
	}
	assert.True(t, samePath("\u017fvc", "svc"))
	assert.False(t, samePath("Stra\u00dfe", "STRASSE"), "a filesystem does not take one letter for two")
	r, err := Resolve(fstest.MapFS{}, f, prodHost, "svc/cmd", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry)
	assert.Equal(t, "long", r.Entry.Name)
}

func TestResolve_Status(t *testing.T) {
	f := exampleFile()
	f.unknown = []string{`"kind" on line 3`}
	r, err := Resolve(fstest.MapFS{}, f, prodHost, "services/checkout/internal", "")
	require.NoError(t, err)
	assert.Equal(t, &Status{
		Linked:      true,
		Code:        StatusLinked,
		Selected:    SelectedByDirectory,
		File:        FileName,
		Environment: prodHost,
		Dir:         "services/checkout/internal",
		Entry:       &f.Environments[prodHost][0],
		Filters: map[string]string{
			"logs":  `in(dt.entity.process_group, array("PROCESS_GROUP-FEDCBA9876543210")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`,
			"spans": `in(dt.entity.service, array("SERVICE-0123456789ABCDEF")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`,
		},
		LiveDebuggerFilters: []map[string][]string{
			{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout"}},
			{"service.name": {"checkout"}},
			{"dt.entity.process_group": {pgID}},
		},
		Warning: `unknown key "kind" on line 3 (a typo, or written by a newer dtctl)`,
		Others:  []string{"ledger"},
		Hosts:   []string{prodHost, stgHost},
	}, r)
}

// A Status is an answer about the file as it was. It does not change when
// the file does, and nothing done to it reaches the file.
func TestResolve_StatusIsACopy(t *testing.T) {
	f := exampleFile()
	f.Environments[prodHost][0].Segments = []Segment{{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"env": {"prod"}}}}
	r, err := Resolve(fstest.MapFS{}, f, prodHost, "services/checkout", "")
	require.NoError(t, err)

	f.Upsert(prodHost, Entry{Name: "checkout", Path: "services/checkout", ServiceNames: []string{"cart"}})
	assert.Equal(t, []string{"checkout"}, r.Entry.ServiceNames, "the entry the filters were rendered from")
	assert.Contains(t, r.Filters["logs"], `service.name == "checkout"`)

	f = exampleFile()
	f.Environments[prodHost][0].Segments = []Segment{{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"env": {"prod"}}}}
	r, err = Resolve(fstest.MapFS{}, f, prodHost, "services/checkout", "checkout")
	require.NoError(t, err)
	r.Entry.ServiceNames[0] = "cart"
	r.Entry.Workloads[0].Name = "cart"
	r.Entry.Segments[0].Variables["env"][0] = "stage"
	r.Entry.Segments[0].Variables["more"] = []string{"x"}
	want := exampleFile()
	want.Environments[prodHost][0].Segments = []Segment{{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"env": {"prod"}}}}
	assert.Equal(t, want, f)

	// Entries hands out a copy too.
	entries := f.Entries(prodHost)
	entries[0].ServiceNames[0] = "cart"
	entries[1] = Entry{}
	assert.Equal(t, want, f)
	assert.Nil(t, f.Entries(stgHost+".invalid"))
}

// countingFS counts what is asked of the repository.
type countingFS struct {
	fstest.MapFS
	calls int
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.calls++
	return c.MapFS.Open(name)
}

func (c *countingFS) Stat(name string) (fs.FileInfo, error) {
	c.calls++
	return c.MapFS.Stat(name)
}

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	c.calls++
	return c.MapFS.ReadDir(name)
}

// The build is read on every scoped query, so it is read only when its
// answer can matter.
func TestResolve_ReadsTheBuildOnlyWhenItMatters(t *testing.T) {
	for _, tc := range []struct {
		name, host, dir, pick string
		reads                 bool
	}{
		{name: "a name the file does not define", host: prodHost, dir: "webproxy-core", pick: "missing"},
		{name: "a named entry that covers the directory", host: prodHost, dir: "webproxy/src", pick: "webproxy"},
		{name: "an environment without entries", host: stgHost, dir: "webproxy-core"},
		{name: "a named entry that does not cover the directory", host: prodHost, dir: "webproxy-core", pick: "webproxy", reads: true},
		{name: "no name", host: prodHost, dir: "webproxy/src", reads: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &countingFS{MapFS: fixture(t, "gradle-multi")}
			_, _ = Resolve(repo, gradleFile(), tc.host, tc.dir, tc.pick)
			assert.Equal(t, tc.reads, repo.calls > 0, "%d calls", repo.calls)
		})
	}
}

// The directory is a path inside the repository, as Location.RelDir is. One
// that is not is the caller's mistake, and is refused: left alone, a
// directory through ".." is covered by the entry it has just left.
func TestResolve_DirMustBeInsideTheRepository(t *testing.T) {
	for _, dir := range []string{"/srv/repo/services/checkout", "services/checkout/..", "services//checkout", "../services/checkout", "./services/checkout", "services/checkout/", ""} {
		done := make(chan error, 1)
		go func() {
			_, err := Resolve(fixture(t, "gradle-multi"), withRepositoryWide(monorepoFile()), prodHost, dir, "")
			done <- err
		}()
		select {
		case err := <-done:
			var invalidErr *InvalidError
			require.ErrorAs(t, err, &invalidErr, "%q", dir)
			assert.Equal(t, fmt.Sprintf("directory %q: is not a clean path relative to the repository root", dir), err.Error())
		case <-time.After(10 * time.Second):
			t.Fatalf("Resolve did not return for %q", dir)
		}
	}
	_, err := Resolve(fstest.MapFS{}, monorepoFile(), prodHost, ".", "")
	require.NoError(t, err, "the root itself")
}

// A caller with no repository to read has no build either.
func TestResolve_WithoutARepository(t *testing.T) {
	r, err := Resolve(nil, monorepoFile(), prodHost, "services/ledger/close", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry)
	assert.Equal(t, "ledger-close", r.Entry.Name)
}

// sharedLibraryRepo is a build whose two deployables both depend on a module
// that lies inside the directory of one of them.
func sharedLibraryRepo() fstest.MapFS {
	uses := &fstest.MapFile{Data: []byte(`dependencies { implementation(project(":app:lib")) }`)}
	return fstest.MapFS{
		"settings.gradle.kts":      {Data: []byte(`include(":app", ":peer", ":app:lib")`)},
		"app/build.gradle.kts":     uses,
		"peer/build.gradle.kts":    uses,
		"app/lib/build.gradle.kts": {Data: []byte("plugins { `java-library` }")},
	}
}

// A module two deployables depend on belongs to neither, also when it lies
// in the directory of one of them: that one is no closer through its path
// than the other is through the build.
func TestResolve_SharedModuleInsideADeployable(t *testing.T) {
	app := Entry{Name: "app", Path: "app", ServiceNames: []string{"app"}}
	peer := Entry{Name: "peer", Path: "peer", ServiceNames: []string{"peer"}}
	for _, entries := range [][]Entry{{app, peer}, {peer, app}} {
		f := &File{Environments: map[string][]Entry{prodHost: entries}}
		r, err := Resolve(sharedLibraryRepo(), f, prodHost, "app/lib/src", "")
		require.NoError(t, err)
		assert.Nil(t, r.Entry)
		assert.True(t, r.Ambiguous())
		assert.Equal(t, "covered by entries "+entries[0].Name+" and "+entries[1].Name+"", r.Reason)

		r, err = Resolve(sharedLibraryRepo(), f, prodHost, "app/src", "")
		require.NoError(t, err)
		require.NotNil(t, r.Entry, "the deployable's own sources are its own")
		assert.Equal(t, "app", r.Entry.Name)
	}

	// An entry on the module itself is closer than either.
	lib := Entry{Name: "lib", Path: "app/lib", ServiceNames: []string{"lib"}}
	r, err := Resolve(sharedLibraryRepo(), &File{Environments: map[string][]Entry{prodHost: {app, peer, lib}}}, prodHost, "app/lib/src", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry)
	assert.Equal(t, "lib", r.Entry.Name)
}

// Two modules whose directories differ only in case hold one directory on a
// filesystem that folds case. Which of them the build is asked about must
// not depend on the order a map is walked in.
func TestResolve_ModuleChoiceIsStable(t *testing.T) {
	prev := foldPathCase
	t.Cleanup(func() { foldPathCase = prev })
	foldPathCase = true
	repo := fstest.MapFS{
		"settings.gradle.kts":  {Data: []byte(`include(":app", ":Lib", ":lib")`)},
		"app/build.gradle.kts": {Data: []byte(`dependencies { implementation(project(":lib")) }`)},
	}
	f := &File{Environments: map[string][]Entry{prodHost: {{Name: "app", Path: "app", ServiceNames: []string{"app"}}}}}
	for range 200 {
		r, err := Resolve(repo, f, prodHost, "lib/src", "")
		require.NoError(t, err)
		require.Equal(t, "covered through the Gradle build: \"app\" depends on \"Lib\"", r.Reason)
	}
}
