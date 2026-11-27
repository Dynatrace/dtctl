package reposcope

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoAppsOneLibrary is a build whose library both deployables depend on, the
// second deployable at appB, with its build file as given.
func twoAppsOneLibrary(appB string, buildB *fstest.MapFile) fstest.MapFS {
	uses := &fstest.MapFile{Data: []byte(`dependencies { implementation(project(":lib")) }`)}
	if buildB == nil {
		buildB = uses
	}
	return fstest.MapFS{
		"settings.gradle":         {Data: []byte("include ':app-a', ':lib', ':" + strings.ReplaceAll(appB, "/", ":") + "'\n")},
		"app-a/build.gradle":      uses,
		"lib/build.gradle":        {Data: []byte("")},
		appB + "/build.gradle":    buildB,
		"lib/src/main/Lib.java":   {Data: []byte("")},
		"app-a/src/main/App.java": {Data: []byte("")},
	}
}

// An entry is selected through the build only when the build was read whole.
// A deployable whose build file was not read may depend on the same library,
// and the library then belongs to neither: read in part, the build would hand
// it to the one deployable that was read, and a query from the library would
// run scoped to the wrong service and say applied.
func TestResolve_SelectsNothingThroughABuildNotReadWhole(t *testing.T) {
	f := &File{Environments: map[string][]Entry{prodHost: {
		{Name: "a", Path: "app-a", ServiceNames: []string{"a"}},
		{Name: "b", Path: "app-b", ServiceNames: []string{"b"}},
	}}}
	// The control: read whole, the library is shared.
	r, err := Resolve(twoAppsOneLibrary("app-b", nil), f, prodHost, "lib", "")
	require.NoError(t, err)
	require.Equal(t, StatusAmbiguousEntry, r.Code)
	require.Equal(t, []string{"a", "b"}, r.Tied)

	// A module far enough below the build that no walk bounded in depth,
	// from whichever directory it counts, reaches its build file.
	deep := "x/" + strings.Repeat("d/", 2*maxDepth) + "app-b"
	manyFiles := twoAppsOneLibrary("app-b", nil)
	for i := range maxFiles {
		manyFiles[fmt.Sprintf("aaa/m%04d/build.gradle", i)] = &fstest.MapFile{}
	}
	for name, tt := range map[string]struct {
		repo fstest.MapFS
		file *File
	}{
		"a build file over the read limit": {
			twoAppsOneLibrary("app-b", &fstest.MapFile{Data: []byte(`dependencies { implementation(project(":lib")) }` + strings.Repeat("\n// padding", maxFileBytes/10))}), f},
		"a build file that is no regular file": {
			twoAppsOneLibrary("app-b", &fstest.MapFile{Data: []byte(`dependencies { implementation(project(":lib")) }`), Mode: 0o644 | fs.ModeNamedPipe}), f},
		"more build files than are read": {manyFiles, f},
		"a module too deep to be walked": {twoAppsOneLibrary(deep, nil), &File{Environments: map[string][]Entry{prodHost: {
			{Name: "a", Path: "app-a", ServiceNames: []string{"a"}},
			{Name: "b", Path: deep, ServiceNames: []string{"b"}},
		}}}},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Resolve(tt.repo, tt.file, prodHost, "lib", "")
			require.NoError(t, err)
			assert.Equal(t, StatusNoEntry, r.Code)
			assert.Nil(t, r.Entry)
			assert.Empty(t, r.Selected)
			assert.Contains(t, r.Reason, `the Gradle build at "." was not read whole (`)
			assert.Contains(t, r.Reason, "so no entry is selected through it")

			// An entry's own path still covers its directory, and a name
			// still picks an entry, without a claim about the build.
			r, err = Resolve(tt.repo, tt.file, prodHost, "app-a/src/main", "")
			require.NoError(t, err)
			require.NotNil(t, r.Entry)
			assert.Equal(t, "a", r.Entry.Name)
			assert.Equal(t, SelectedByDirectory, r.Selected)
			r, err = Resolve(tt.repo, tt.file, prodHost, "lib", "a")
			require.NoError(t, err)
			assert.Equal(t, `selected by name; its path "app-a" does not cover "lib"`, r.Reason)
		})
	}

	// The repository-wide entry applies from the library, and says that the
	// build could have named a closer one.
	wide := &File{Environments: map[string][]Entry{prodHost: append(f.Entries(prodHost), Entry{Name: "all", ServiceNames: []string{"all"}})}}
	r, err = Resolve(manyFiles, wide, prodHost, "lib", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry)
	assert.Equal(t, "all", r.Entry.Name)
	assert.Contains(t, r.Reason, "was not read whole")
}

// Whether a build was read whole does not depend on where in the repository
// it lies: the walk counts its depth from the build's own directory, so a
// build further down than a walk from the repository root would go is read
// with its modules, and an entry is selected through it. Within such a build
// a module the walk does not reach still leaves it not read whole.
func TestResolve_ABuildDeepInTheRepositoryIsReadWhole(t *testing.T) {
	base := strings.TrimSuffix(strings.Repeat("d/", maxDepth+2), "/")
	under := func(repo fstest.MapFS) fstest.MapFS {
		moved := fstest.MapFS{}
		for name, file := range repo {
			moved[base+"/"+name] = file
		}
		return moved
	}
	entries := func(appB string) *File {
		return &File{Environments: map[string][]Entry{prodHost: {
			{Name: "a", Path: base + "/app-a", ServiceNames: []string{"a"}},
			{Name: "b", Path: base + "/" + appB, ServiceNames: []string{"b"}},
		}}}
	}

	// app-b does not use the library, so it is app-a's alone.
	alone := &fstest.MapFile{Data: []byte("")}
	r, err := Resolve(under(twoAppsOneLibrary("app-b", alone)), entries("app-b"), prodHost, base+"/lib", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry, r.Reason)
	assert.Equal(t, "a", r.Entry.Name)
	assert.NotContains(t, r.Reason, "was not read whole")

	// The same build with app-b further below it than its walk goes.
	deep := "x/" + strings.Repeat("e/", 2*maxDepth) + "app-b"
	r, err = Resolve(under(twoAppsOneLibrary(deep, alone)), entries(deep), prodHost, base+"/lib", "")
	require.NoError(t, err)
	assert.Nil(t, r.Entry)
	assert.Contains(t, r.Reason, fmt.Sprintf("the Gradle build at %q was not read whole (", base))
}

// A literal project path names one module, by its directory. Only a
// type-safe accessor is matched by words, since projects.billingApi is how
// Gradle spells billing-api.
func TestResolve_LiteralProjectPathNamesOneModule(t *testing.T) {
	repo := fstest.MapFS{
		"settings.gradle":          {Data: []byte("include ':app', ':billing-api', ':billing_api', ':order-api'\n")},
		"app/build.gradle":         {Data: []byte("dependencies {\n  implementation project(':billing-api')\n  implementation projects.orderApi\n}\n")},
		"billing-api/build.gradle": {Data: []byte("")},
		"billing_api/build.gradle": {Data: []byte("")},
		"order-api/build.gradle":   {Data: []byte("")},
	}
	f := &File{Environments: map[string][]Entry{prodHost: {{Name: "app", Path: "app", ServiceNames: []string{"app"}}}}}
	for dir, linked := range map[string]bool{"billing-api": true, "order-api": true, "billing_api": false} {
		r, err := Resolve(repo, f, prodHost, dir, "")
		require.NoError(t, err)
		assert.Equal(t, linked, r.Linked, dir)
	}
}

// The closest entry is the one whose path has the most segments. Counted in
// bytes, a parent whose spelling is longer than the same path folded or
// composed outranks its child.
func TestResolve_ClosestIsDeepestNotLongest(t *testing.T) {
	prev := foldPathCase
	t.Cleanup(func() { foldPathCase = prev })

	// U+017F folds to s and is two bytes, so the parent is spelled longer
	// than the child's whole path.
	foldPathCase = true
	parent := Entry{Name: "parent", Path: "ſſſſ", ServiceNames: []string{"p"}}
	child := Entry{Name: "child", Path: "ssss/a", ServiceNames: []string{"c"}}
	for _, entries := range [][]Entry{{parent, child}, {child, parent}} {
		f := &File{Environments: map[string][]Entry{prodHost: entries}}
		require.Nil(t, f.check())
		r, err := Resolve(nil, f, prodHost, "ssss/a", "")
		require.NoError(t, err)
		require.NotNil(t, r.Entry)
		assert.Equal(t, "child", r.Entry.Name)
	}

	// A module directory in decomposed Unicode is longer than the composed
	// path of an entry below it, on every filesystem.
	foldPathCase = false
	for _, letters := range []int{2, 3} {
		composed, decomposed := strings.Repeat("é", letters), strings.Repeat("é", letters)
		repo := fstest.MapFS{
			"settings.gradle":             {Data: []byte("include ':app', ':" + decomposed + "'\n")},
			"app/build.gradle":            {Data: []byte("dependencies { implementation project(':" + decomposed + "') }\n")},
			decomposed + "/build.gradle":  {Data: []byte("")},
			decomposed + "/a/Source.java": {Data: []byte("")},
		}
		f := &File{Environments: map[string][]Entry{prodHost: {
			{Name: "app", Path: "app", ServiceNames: []string{"app"}},
			{Name: "sub", Path: composed + "/a", ServiceNames: []string{"sub"}},
		}}}
		require.Nil(t, f.check())
		r, err := Resolve(repo, f, prodHost, composed+"/a", "")
		require.NoError(t, err)
		require.NotNil(t, r.Entry, "%d letters: %s", letters, r.Reason)
		assert.Equal(t, "sub", r.Entry.Name, "%d letters", letters)
	}
}

// A Status is a result, and a result is printed and read back. The reason an
// entry renders no Live Debugger filters is still there afterwards.
func TestStatus_LiveDebuggerReasonSurvivesJSON(t *testing.T) {
	f := &File{Environments: map[string][]Entry{prodHost: {{Name: "seg", Segments: []Segment{{ID: "Ab3dEf9hIjk"}}}}}}
	r, err := Resolve(nil, f, prodHost, ".", "")
	require.NoError(t, err)
	_, ok, want := r.LiveDebuggerFilterSets()
	require.False(t, ok)
	require.NotEmpty(t, want)

	data, err := json.Marshal(r)
	require.NoError(t, err)
	var back Status
	require.NoError(t, json.Unmarshal(data, &back))
	_, ok, reason := back.LiveDebuggerFilterSets()
	assert.False(t, ok)
	assert.Equal(t, want, reason)
}

// A directory name is whatever the filesystem holds, and on Linux that need
// not be UTF-8. Such a directory is still a directory of the repository: the
// entry that covers the whole of it applies there, and no build is read.
func TestResolve_DirectoryThatIsNotUTF8(t *testing.T) {
	f := &File{Environments: map[string][]Entry{prodHost: {
		{Name: "all", ServiceNames: []string{"all"}},
		{Name: "svc", Path: "services", ServiceNames: []string{"svc"}},
	}}}
	repo := &countingFS{MapFS: fstest.MapFS{"settings.gradle": {Data: []byte("include ':services'\n")}}}
	defer func() { assert.Zero(t, repo.calls, "an fs.FS cannot name the directory, so nothing is asked of it") }()
	r, err := Resolve(repo, f, prodHost, "other/\xff", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry)
	assert.Equal(t, "all", r.Entry.Name)
	r, err = Resolve(repo, f, prodHost, "services/\xff", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry)
	assert.Equal(t, "svc", r.Entry.Name)

	for _, dir := range []string{"", "/abs", "a/../b", "../up", "a//b", "a/", "./a"} {
		_, err := Resolve(repo, f, prodHost, dir, "")
		var invalidErr *InvalidError
		require.ErrorAs(t, err, &invalidErr, "%q", dir)
	}
}

// Reading a build's references is linear in its modules: resolving reads the
// build on every scoped query. Held by how the time grows from one build to
// one four times its size, not by a duration, which a slow machine or the
// race detector would stretch: comparing every reference with every module
// took sixteen times as long for the larger one.
func TestGradleBuildAt_ManyModules(t *testing.T) {
	const refs = 40
	build := func(modules int) fstest.MapFS {
		fsys := fstest.MapFS{}
		var settings strings.Builder
		for i := range modules {
			fmt.Fprintf(&settings, "include ':module-%d'\n", i)
			var file strings.Builder
			for j := range refs {
				fmt.Fprintf(&file, "implementation project(':module-%d')\nimplementation projects.module%d\n", (i+j+1)%modules, i+j)
			}
			fsys[fmt.Sprintf("module-%d/build.gradle", i)] = &fstest.MapFile{Data: []byte(file.String())}
		}
		fsys["settings.gradle"] = &fstest.MapFile{Data: []byte(settings.String())}
		return fsys
	}
	// The fastest of three reads, so that one pause does not decide it.
	read := func(fsys fstest.MapFS, modules int) time.Duration {
		fastest := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			b := gradleBuildAt(fsys, "module-0")
			fastest = min(fastest, time.Since(start))
			require.NotNil(t, b)
			require.Len(t, b.modules, modules)
			require.Contains(t, b.modules["module-0"], "module-1")
			require.Len(t, b.modules["module-0"], refs)
		}
		return fastest
	}
	const small, large = 120, 480
	one, four := read(build(small), small), read(build(large), large)
	assert.Less(t, four, 9*one, "%d modules took %s and %d took %s", small, one, large, four)
}
