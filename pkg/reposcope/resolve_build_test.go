package reposcope

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// build on every scoped query.
func TestGradleBuildAt_ManyModules(t *testing.T) {
	const modules, refs = 500, 40
	fsys := fstest.MapFS{}
	var settings strings.Builder
	for i := range modules {
		fmt.Fprintf(&settings, "include ':module-%d'\n", i)
		var build strings.Builder
		for j := range refs {
			fmt.Fprintf(&build, "implementation project(':module-%d')\nimplementation projects.module%d\n", (i+j+1)%modules, i+j)
		}
		fsys[fmt.Sprintf("module-%d/build.gradle", i)] = &fstest.MapFile{Data: []byte(build.String())}
	}
	fsys["settings.gradle"] = &fstest.MapFile{Data: []byte(settings.String())}
	start := time.Now()
	b := gradleBuildAt(fsys, "module-0")
	elapsed := time.Since(start)
	require.NotNil(t, b)
	assert.Len(t, b.modules, modules)
	assert.Contains(t, b.modules["module-0"], "module-1")
	assert.Len(t, b.modules["module-0"], refs)
	assert.Less(t, elapsed, 500*time.Millisecond)
}
