package output

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// streamInjectingPackages are the packages whose types let a caller choose the
// streams and the filesystem they use (WithStreams, WithStderr, WithVFS, ...).
// Their paths are relative to this one.
var streamInjectingPackages = []string{
	"../exec",
	"../apply",
	"../diff",
	"../wait",
	"../prompt",
	"../vfs",
	"../resources/appengine",
	"../resources/analyzer",
	"../resources/previewprocessor",
}

// processStreamDefaults lists the functions that may name the process streams,
// as "<package>/<file>:<function>": each is where a type's default is defined,
// and so the one place the stream a caller did not choose is resolved.
var processStreamDefaults = map[string]string{
	"exec/dql.go:outW":                      "DQLExecutor's stdout when WithStreams was not called",
	"exec/dql.go:errW":                      "DQLExecutor's stderr when WithStreams was not called",
	"apply/applier.go:stderrW":              "Applier's stderr when WithStderr was not called",
	"apply/applier.go:hookStdoutWriter":     "where apply hooks write when WithHookOutputs was not called",
	"apply/applier.go:hookStderrWriter":     "where apply hooks write when WithHookOutputs was not called",
	"wait/query_waiter.go:NewQueryWaiter":   "WaitConfig.ProgressOut when none was given",
	"prompt/confirm.go:Confirm":             "the process-stream form of ConfirmWith",
	"prompt/confirm.go:ConfirmDeletion":     "the process-stream form of ConfirmDeletionWith",
	"prompt/confirm.go:ConfirmDataDeletion": "the process-stream form of ConfirmDataDeletionWith",
	"vfs/vfs.go:StdinReader":                "Env's stdin when none was given",
}

// outputProcessStreamFuncs are the pkg/output functions that write to a process
// stream. Each has an explicit form (FprintWarning, NewPrinterWithOpts, ...).
var outputProcessStreamFuncs = map[string]bool{
	"PrintSuccess": true, "PrintWarning": true, "PrintHumanError": true, "PrintHint": true, "PrintInfo": true,
	"DescribeSection": true, "DescribeKV": true,
	"NewPrinter": true, "NewProgressReporter": true, "NewWatchPrinter": true,
}

// TestStreamInjectingPackagesDoNotWriteToTheProcessStreams keeps the contract of
// the injectable types honest. A type that accepts a writer or a filesystem has
// promised that nothing it does reaches the process's own streams; a single
// output.PrintWarning on a path it can take breaks that promise for every host
// that gives each invocation streams of its own, and no test of the type's
// output notices, because the text still appears — on the wrong stream.
//
// Use the explicit form (output.FprintWarning(w, ...)) with the type's writer.
// A function that defines a default belongs in processStreamDefaults, with the
// reason.
func TestStreamInjectingPackagesDoNotWriteToTheProcessStreams(t *testing.T) {
	used := map[string]bool{}
	var violations []string

	for _, dir := range streamInjectingPackages {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			key := strings.TrimPrefix(filepath.ToSlash(dir), "../") + "/" + name

			for _, decl := range file.Decls {
				fn, _ := decl.(*ast.FuncDecl)
				funcName := ""
				if fn != nil {
					funcName = fn.Name.Name
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					what := processStreamUse(n)
					if what == "" {
						return true
					}
					if _, ok := processStreamDefaults[key+":"+funcName]; ok && funcName != "" {
						used[key+":"+funcName] = true
						return true
					}
					violations = append(violations, key+": "+what+" in "+describeFunc(funcName))
					return true
				})
			}
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: write through the type's own writer, or add the function to processStreamDefaults with the reason", v)
	}
	for key := range processStreamDefaults {
		if !used[key] {
			t.Errorf("processStreamDefaults lists %s, which no longer names a process stream: remove it", key)
		}
	}
}

func describeFunc(name string) string {
	if name == "" {
		return "a package-level declaration"
	}
	return name
}

// processStreamUse names the process-stream use n makes, or returns "".
func processStreamUse(n ast.Node) string {
	switch n := n.(type) {
	case *ast.SelectorExpr:
		if id, ok := n.X.(*ast.Ident); ok && id.Name == "os" {
			switch n.Sel.Name {
			case "Stdout", "Stderr", "Stdin":
				return "os." + n.Sel.Name
			}
		}
	case *ast.CallExpr:
		sel, ok := n.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return ""
		}
		switch {
		case id.Name == "output" && outputProcessStreamFuncs[sel.Sel.Name]:
			return "output." + sel.Sel.Name
		case id.Name == "fmt" && (sel.Sel.Name == "Print" || sel.Sel.Name == "Printf" || sel.Sel.Name == "Println"):
			return "fmt." + sel.Sel.Name
		}
	}
	return ""
}
