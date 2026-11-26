package reposcope_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dynatrace/dtctl/pkg/dql"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// Example is the read path a program other than dtctl takes to honour a
// checkout's scope file: locate it from a directory, load it, resolve the
// entry for one environment host, and insert the entry's filter into a query.
// None of these calls writes, and none reaches an environment.
func Example() {
	repo, err := os.MkdirTemp("", "reposcope-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(repo)
	dir := filepath.Join(repo, "services", "checkout")
	must(os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	must(os.MkdirAll(dir, 0o755))
	must(os.WriteFile(filepath.Join(repo, reposcope.FileName), []byte(`environments:
  abc12345.apps.dynatrace.com:
    - name: checkout
      path: services/checkout
      workloads:
        - namespace: payments
          name: checkout
`), 0o644))

	loc, ok, err := reposcope.Locate(dir)
	must(err)
	if !ok {
		panic("not in a repository")
	}
	file, err := reposcope.Load(loc)
	must(err)
	status, err := reposcope.Resolve(reposcope.RepoFS(loc), file, "abc12345.apps.dynatrace.com", loc.RelDir, "")
	must(err)
	if status.Entry == nil {
		fmt.Println("unscoped:", status.Reason)
		return
	}

	rewrite := dql.InsertFilter("fetch logs | limit 20", reposcope.Filters(status.Entry))
	fmt.Println(status.Entry.Name, rewrite.Outcome)
	fmt.Println(rewrite.Query)

	// A query the filter cannot be placed in comes back as typed, with the
	// reason; the caller must not claim it was scoped.
	rewrite = dql.InsertFilter("timeseries avg(dt.host.cpu.usage)", reposcope.Filters(status.Entry))
	fmt.Println(rewrite.Outcome, rewrite.Query)
	// Output:
	// checkout applied
	// fetch logs | filter ((k8s.namespace.name == "payments" and k8s.workload.name == "checkout"))
	// | limit 20
	// not_fetch timeseries avg(dt.host.cpu.usage)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
