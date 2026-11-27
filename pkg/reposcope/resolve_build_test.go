package reposcope

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

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
