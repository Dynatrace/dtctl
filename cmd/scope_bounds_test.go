package cmd

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/auth"
)

// localCommands are the runnable commands declared, or inheriting the
// declaration, with markLocal. Parse reports maximum scopes [] for them, so a
// host gives them a token without scopes: a command listed here that does
// reach the platform fails for every caller.
var localCommands = []string{
	"alias delete",
	"alias export",
	"alias import",
	"alias list",
	"alias set",
	"auth login",
	"auth logout",
	"auth refresh",
	"auth status",
	"commands",
	"commands howto",
	"completion",
	"config current-context",
	"config delete-context",
	"config delete-credentials",
	"config describe-context",
	"config get-contexts",
	"config init",
	"config list-development",
	"config migrate-tokens",
	"config set",
	"config set-context",
	"config set-credentials",
	"config use-context",
	"config view",
	"ctx",
	"ctx current",
	"ctx delete",
	"ctx describe",
	"ctx set",
	"ctx token",
	"inspect",
	"plugin list",
	"skills install",
	"skills status",
	"skills uninstall",
	"update settings",
	"usage",
	"usage reset",
	"version",
}

// TestLocalCommandsArePinned holds the set of local commands to the list
// above. A new subcommand of a local group inherits the mark; the list makes
// that a decision someone took.
func TestLocalCommandsArePinned(t *testing.T) {
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	var got []string
	walkCommands(root, func(c *cobra.Command) {
		if isParseRunnable(c) && isLocal(c) {
			got = append(got, commandPathRelative(c, root))
		}
	})
	sort.Strings(got)
	assert.Equal(t, localCommands, got,
		"a command was added to or removed from the local set: update localCommands if it never reaches the platform, else remove its markLocal")
}

// isParseRunnable reports whether c has a body Parse records: runnable, and
// not a group that only refuses a missing subcommand.
func isParseRunnable(c *cobra.Command) bool {
	return c.HasParent() && c.Runnable() && (c.RunE == nil || !isRefusal(c))
}

// noCatalogEntryCommands are the commands that reach the platform but have no
// scope catalog entry and no declaration, each with the reason. They report
// minimum [] and maximum ["*"]. The list can only shrink: give a new command
// a catalog entry (auth.ResourceScopes) or a declaration instead.
var noCatalogEntryCommands = map[string]string{
	"auth whoami":  "resolves the current user (app-engine:apps:run with a platform token; a JWT is decoded instead)",
	"describe api": "reads the environment's API index and specifications; needs a valid token, no scope",
	"get apis":     "reads the environment's API index; needs a valid token, no scope",
	"doctor":       "checks that the environment answers and resolves the current user (app-engine:apps:run with a platform token)",
}

// TestScopeBoundsOverTheTree holds the scope bounds of every command: both
// are non-nil, every minimum scope matches a maximum pattern, only a local
// command reports maximum [], and a command reports ["*"] only when it is
// declared to or is listed in noCatalogEntryCommands.
func TestScopeBoundsOverTheTree(t *testing.T) {
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	unlisted := maps.Clone(noCatalogEntryCommands)
	walkCommands(root, func(c *cobra.Command) {
		if !isParseRunnable(c) {
			return
		}
		path := commandPathRelative(c, root)
		verb, resource := verbResource(c)
		minimum, maximum := scopeBounds(c, verb, resource)
		require.NotNil(t, minimum, path)
		require.NotNil(t, maximum, path)
		for _, s := range minimum {
			if !slices.ContainsFunc(maximum, func(p string) bool { return auth.ScopeMatches(p, s) }) {
				t.Errorf("%s: minimum scope %s matches no maximum pattern %v", path, s, maximum)
			}
		}
		local := isLocal(c)
		if local != (len(maximum) == 0) {
			t.Errorf("%s: local is %v, but maximum is %v", path, local, maximum)
		}
		declared := c.Annotations[annotationScopes] == scopesInputDependent ||
			c.Annotations[annotationScopes] == scopesAny || perCallScopeCommands[verb+" "+resource]
		if slices.Equal(maximum, []string{anyScope}) && !declared {
			if _, ok := noCatalogEntryCommands[path]; !ok {
				t.Errorf("%s reaches the platform, but has no catalog entry: add one to auth.ResourceScopes, or markLocal it if it never reaches the platform", path)
			}
			delete(unlisted, path)
		}
	})
	for path := range unlisted {
		t.Errorf("noCatalogEntryCommands lists %q, which now has known scopes or is gone: remove it", path)
	}
}

// TestDQLStatementCommandsAreDeclared holds that a command whose catalog
// requirement is the DQL scope list is declared with markScopesDQL, so it
// reports maximum storage:*:read instead of a fixed list that misses the
// tables it is not named after.
func TestDQLStatementCommandsAreDeclared(t *testing.T) {
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	walkCommands(root, func(c *cobra.Command) {
		if !isParseRunnable(c) {
			return
		}
		verb, resource := verbResource(c)
		required, _ := requiredScopesIn(root, verb, resource)
		if slices.Equal(required, auth.QueryScopes) && c.Annotations[annotationScopes] != scopesDQL {
			t.Errorf("%s needs the DQL scopes; declare it with markScopesDQL", commandPathRelative(c, root))
		}
	})
}

// TestScopesDQLCoversFilterSegments holds that the maximum of a markScopesDQL
// command covers the filter segments it reads (query --segment, inventory):
// every read scope of the segment resource matches auth.QueryScopePattern.
func TestScopesDQLCoversFilterSegments(t *testing.T) {
	scopes := auth.ScopesForResource("segment", auth.AccessRead)
	require.NotEmpty(t, scopes)
	for _, s := range scopes {
		assert.True(t, auth.ScopeMatches(auth.QueryScopePattern, s), "%s does not match %s", s, auth.QueryScopePattern)
	}
}

// TestReadAlternativesKeepTheVerbsOwnScopes holds that the alternatives of
// the read level make only the read list optional for a read: a delete whose
// own list repeats the read scope (scheduling-rule) keeps it in its minimum.
func TestReadAlternativesKeepTheVerbsOwnScopes(t *testing.T) {
	auth.ResourceScopes["fakerule"] = auth.AccessScopes{
		Read:         []string{"fake:rules:read"},
		Write:        []string{"fake:rules:write"},
		Delete:       []string{"fake:rules:write", "fake:rules:read"},
		Alternatives: map[auth.Access][][]string{auth.AccessRead: {{"fake:other:read"}}},
	}
	t.Cleanup(func() { delete(auth.ResourceScopes, "fakerule") })
	root := &cobra.Command{Use: "dtctl"}
	for _, verb := range []string{"get", "delete"} {
		v := &cobra.Command{Use: verb}
		v.AddCommand(&cobra.Command{Use: "fakerule", RunE: func(*cobra.Command, []string) error { return nil }})
		root.AddCommand(v)
	}

	// The minimum is the catalog list without the optional scopes
	// (scopeBounds); the catalog is built from the real tree, so the fake
	// resource is checked through resourceScopes.
	get, _, err := root.Find([]string{"get", "fakerule"})
	require.NoError(t, err)
	usable, optional := resourceScopes(get, "get")
	assert.ElementsMatch(t, []string{"fake:rules:read", "fake:other:read"}, usable, "get usable")
	assert.Equal(t, []string{"fake:rules:read"}, optional, "get optional")

	del, _, err := root.Find([]string{"delete", "fakerule"})
	require.NoError(t, err)
	usable, optional = resourceScopes(del, "delete")
	assert.ElementsMatch(t, []string{"fake:rules:write", "fake:rules:read", "fake:other:read"}, usable, "delete usable")
	assert.Empty(t, optional, "delete optional: the delete minimum keeps fake:rules:read")
}

// dqlSites maps every function in cmd/ that builds a DQL executor to the
// commands that call it. An entry without commands names the reason it runs
// no query.
var dqlSites = map[string][]string{
	"describe_anomalydetector.go:printAnomalyDetectorRecentProblems": {"describe anomaly-detector"},
	"describe_aws.go:printAWSMonitoringConfigStatus":                 {"describe aws monitoring"},
	"describe_gcp.go:printGCPMonitoringConfigStatus":                 {"describe gcp monitoring"},
	"describe.go:printAzureMonitoringConfigStatus":                   {"describe azure monitoring"},
	"exec_dql.go:newExecDQLCmd":                                      {"exec dql"},
	"get_snapshots.go:newGetSnapshotsCmd":                            {"get snapshots"},
	"inventory.go:newInventoryRunner":                                {"inventory", "inventory arrivals"},
	"query.go:newQueryCmd":                                           {"query"},
	"verify_query.go:newVerifyQueryCmd":                              {"verify query"},
	"wait.go:newWaitQueryCmd":                                        {"wait query"},
	"get_lookups.go:printLookupNotifications":                        nil, // prints the notifications of a finished query, runs none
}

// dqlPackages maps every file below pkg/ (outside pkg/exec) that builds or
// holds a DQL executor, or a handler of the query API, to the commands that
// reach it.
var dqlPackages = map[string][]string{
	"pkg/resources/lookup/lookup.go": {"get lookups", "describe lookup", "delete lookup"},
	"pkg/wait/query_waiter.go":       {"wait query"},
}

// dqlRunners are the identifiers through which code runs DQL, keyed by the
// import path of their package.
var dqlRunners = map[string][]string{
	"github.com/Dynatrace/dtctl/pkg/exec":      {"DQLExecutor", "NewDQLExecutor"},
	"github.com/Dynatrace/dtctl/sdk/api/query": {"Handler", "NewHandler"},
}

// referencesDQLRunner reports whether f names an identifier of dqlRunners
// through its import, so that a file given an executor counts as well as one
// that builds it.
func referencesDQLRunner(f *ast.File) bool {
	runners := map[string][]string{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		names, ok := dqlRunners[path]
		if !ok {
			continue
		}
		local := filepath.Base(path)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		runners[local] = names
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && slices.Contains(runners[pkg.Name], sel.Sel.Name) {
				found = true
			}
		}
		return !found
	})
	return found
}

// TestPackageDQLRunnersAreDeclared is TestDQLRunnersAreDeclared for DQL run
// below pkg/, which the cmd/ scan does not see: a file that builds or holds a
// DQL executor or a query API handler (dqlRunners). It skips pkg/exec, the
// executor itself. sdk/ is not scanned: it cannot import
// the executor, and runs DQL only through a runner cmd/ gives it
// (sdk/inventory), which the cmd/ scan sees.
func TestPackageDQLRunnersAreDeclared(t *testing.T) {
	found := map[string]bool{}
	err := filepath.WalkDir("../pkg", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := repoRelative(path)
		if d.IsDir() {
			if rel == "pkg/exec" || skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		if referencesDQLRunner(f) {
			found[rel] = true
			if _, ok := dqlPackages[rel]; !ok {
				t.Errorf("%s runs DQL; declare the commands that reach it with markRunsDQL and list them in dqlPackages", rel)
			}
		}
		return nil
	})
	require.NoError(t, err)
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	for file, paths := range dqlPackages {
		if !found[file] {
			t.Errorf("%s no longer runs DQL: remove its entry", file)
		}
		for _, path := range paths {
			c, _, err := root.Find(strings.Fields(path))
			require.NoError(t, err, path)
			if c.Annotations[annotationScopes] != scopesDQL && c.Annotations[annotationRunsDQL] != "true" {
				t.Errorf("%s runs DQL through %s; declare it with markRunsDQL", path, file)
			}
		}
	}
}

// pinnedBounds pins the bounds that the catalog alone does not explain: the
// commands whose calls the server carries out with the caller's token, and the
// lookup commands, which dtctl reads through DQL. Each was checked against the
// specification the environment publishes.
var pinnedBounds = map[string][2][]string{
	// The analyzer runs with the caller's token and can do any work, so a
	// fixed list breaks it (an analyzer's DQL failed without storage:logs:read).
	"exec analyzer": {{"davis:analyzers:execute"}, {"*"}},
	// The detector's analyzer input carries a query.
	"create anomaly-detector": {{"settings:objects:write"}, {"*"}},
	"edit anomaly-detector":   {{"settings:objects:write"}, {"*"}},
	// The function runs code the caller supplies with the caller's token.
	"exec function": {{"app-engine:functions:run"}, {"*"}},
	// Lookups are listed and read through DQL (fetch dt.system.files, load).
	"get lookups":     {{"storage:files:read"}, {"storage:*:read", "storage:files:read"}},
	"describe lookup": {{"storage:files:read"}, {"storage:*:read", "storage:files:read"}},
	"delete lookup":   {{"storage:files:delete"}, {"storage:*:read", "storage:files:delete", "storage:files:read"}},
}

// TestPinnedBounds holds pinnedBounds.
func TestPinnedBounds(t *testing.T) {
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	for path, want := range pinnedBounds {
		c, _, err := root.Find(strings.Fields(path))
		require.NoError(t, err, path)
		require.Equal(t, path, commandPathRelative(c, root))
		verb, resource := verbResource(c)
		minimum, maximum := scopeBounds(c, verb, resource)
		assert.Equal(t, want[0], minimum, "%s minimum", path)
		assert.Equal(t, want[1], maximum, "%s maximum", path)
	}
	// Evaluation runs the SLO's indicator DQL with the caller's token. Only
	// the Grail read is pinned here: the SLO scopes are the catalog's.
	c, _, err := root.Find([]string{"exec", "slo"})
	require.NoError(t, err)
	_, maximum := scopeBounds(c, "exec", "slo")
	assert.Contains(t, maximum, auth.QueryScopePattern, "exec slo maximum")
}

// TestDQLRunnersAreDeclared holds that every function in cmd/ that builds a
// DQL executor is listed in dqlSites, and that each command it lists can use
// the Grail read scopes: it is declared with markScopesDQL (it runs DQL only)
// or markRunsDQL (it adds a Grail section), so its maximum holds
// storage:*:read. A command without would get a token that cannot read what
// it queries. DQL run below pkg/ is TestPackageDQLRunnersAreDeclared's.
func TestDQLRunnersAreDeclared(t *testing.T) {
	wrappers := map[string]bool{"newDQLExecutor": true, "newDQLExecutorFromConfig": true, "NewDQLExecutorFromConfig": true}
	found := callSites(t, wrappers, func(call *ast.CallExpr) bool {
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			return wrappers[fun.Name]
		case *ast.SelectorExpr:
			return fun.Sel.Name == "NewDQLExecutor"
		}
		return false
	})
	for site, pos := range found {
		if _, ok := dqlSites[site]; !ok {
			t.Errorf("%s: %s runs DQL; declare its command with markRunsDQL and list it in dqlSites", pos, site)
		}
	}
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	for site, paths := range dqlSites {
		if _, ok := found[site]; !ok {
			t.Errorf("%s no longer builds a DQL executor: remove its entry", site)
		}
		for _, path := range paths {
			c, _, err := root.Find(strings.Fields(path))
			require.NoError(t, err, path)
			require.Equal(t, path, commandPathRelative(c, root))
			if c.Annotations[annotationScopes] != scopesDQL && c.Annotations[annotationRunsDQL] != "true" {
				t.Errorf("%s runs DQL; declare it with markScopesDQL or markRunsDQL", path)
			}
		}
	}
}

// callSites returns the functions in the non-test files of cmd/ that make a
// call match accepts, keyed "<file>:<function>", with the position of the
// first such call. Functions named in skip are wrappers: their callers are
// the sites.
func callSites(t *testing.T, skip map[string]bool, match func(*ast.CallExpr) bool) map[string]token.Position {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	found := map[string]token.Position{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || skip[fn.Name.Name] {
				continue
			}
			site := name + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && match(call) {
					if _, seen := found[site]; !seen {
						found[site] = fset.Position(call.Pos())
					}
				}
				return true
			})
		}
	}
	return found
}

// userSites maps every function in cmd/ that finds the current user
// (client.CurrentUserID, client.CurrentUser) to the commands that call it.
var userSites = map[string][]string{
	"auth.go:newAuthWhoamiCmd":                           {"auth whoami"},
	"create_anomalydetector.go:currentActor":             {"create anomaly-detector"},
	"doctor.go:runDoctorChecksWithClient":                {"doctor"},
	"edit_documents.go:newEditDashboardCmd":              {"edit dashboard"},
	"edit_documents.go:newEditDocumentCmd":               {"edit document"},
	"edit_documents.go:newEditNotebookCmd":               {"edit notebook"},
	"edit_segments.go:newEditSegmentCmd":                 {"edit segment"},
	"edit_workflows.go:newEditWorkflowCmd":               {"edit workflow"},
	"get_documents.go:buildDocumentFilters":              {"get dashboards", "get documents", "get notebooks"},
	"get_documents.go:newDeleteDashboardCmd":             {"delete dashboard"},
	"get_documents.go:newDeleteDocumentCmd":              {"delete document"},
	"get_documents.go:newDeleteNotebookCmd":              {"delete notebook"},
	"get_notifications.go:newDeleteNotificationCmd":      {"delete notification"},
	"get_scheduling_rules.go:newDeleteSchedulingRuleCmd": {"delete scheduling-rule"},
	"get_segments.go:newDeleteSegmentCmd":                {"delete segment"},
	"get_workflows.go:newDeleteWorkflowCmd":              {"delete workflow"},
	"get_workflows.go:newGetWorkflowsCmd":                {"get workflows"},
	"restore.go:newRestoreDashboardCmd":                  {"restore dashboard"},
	"restore.go:newRestoreDocumentCmd":                   {"restore document"},
	"restore.go:newRestoreNotebookCmd":                   {"restore notebook"},
	"restore.go:newRestoreWorkflowCmd":                   {"restore workflow"},
	"share.go:runShareDocument":                          {"share dashboard", "share document", "share notebook"},
	"share.go:runUnshareDocument":                        {"unshare dashboard", "unshare document", "unshare notebook"},
}

// userPackages maps every file below pkg/ that finds the current user to the
// commands that reach it.
var userPackages = map[string][]string{
	"pkg/apply/applier.go": {"apply", "update document"}, // NewApplier
}

// TestUserResolversAreDeclared holds that every command that finds the
// current user can use auth.CurrentUserScope. A platform token cannot be
// decoded instead, so without the scope an ownership check sees an unknown
// owner and --mine fails. Declare such a command with markResolvesUser. It
// scans the non-test files of cmd/ (userSites) and below pkg/ (userPackages).
func TestUserResolversAreDeclared(t *testing.T) {
	found := callSites(t, nil, func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && (sel.Sel.Name == "CurrentUserID" || sel.Sel.Name == "CurrentUser")
	})
	for site, pos := range found {
		if _, ok := userSites[site]; !ok {
			t.Errorf("%s: %s finds the current user; declare its command with markResolvesUser and list it in userSites", pos, site)
		}
	}
	for site := range userSites {
		if _, ok := found[site]; !ok {
			t.Errorf("%s no longer finds the current user: remove its entry", site)
		}
	}
	foundPackages := map[string]bool{}
	err := filepath.WalkDir("../pkg", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := repoRelative(path)
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return err
		}
		if strings.Contains(string(src), ".CurrentUserID(") || strings.Contains(string(src), ".CurrentUser(") {
			foundPackages[rel] = true
			if _, ok := userPackages[rel]; !ok {
				t.Errorf("%s finds the current user; declare the commands that reach it with markResolvesUser and list them in userPackages", rel)
			}
		}
		return nil
	})
	require.NoError(t, err)
	for file := range userPackages {
		if !foundPackages[file] {
			t.Errorf("%s no longer finds the current user: remove its entry", file)
		}
	}
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	for _, paths := range slices.Concat(slices.Collect(maps.Values(userSites)), slices.Collect(maps.Values(userPackages))) {
		for _, path := range paths {
			c, _, err := root.Find(strings.Fields(path))
			require.NoError(t, err, path)
			require.Equal(t, path, commandPathRelative(c, root))
			if flag := c.Annotations[annotationResolvesUser]; flag != "" && flag != "true" {
				require.NoError(t, c.Flags().Set(flag, "true"), path)
			}
			verb, resource := verbResource(c)
			_, maximum := scopeBounds(c, verb, resource)
			if !slices.ContainsFunc(maximum, func(p string) bool { return auth.ScopeMatches(p, auth.CurrentUserScope) }) {
				t.Errorf("%s finds the current user, but its maximum %v lacks %s; declare it with markResolvesUser",
					path, maximum, auth.CurrentUserScope)
			}
		}
	}
}
