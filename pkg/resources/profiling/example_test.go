package profiling_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/Dynatrace/dtctl/pkg/resources/profiling"
	"github.com/Dynatrace/dtctl/sdk/session"
)

// The call sequence of a program that runs a hotspots analysis itself rather
// than through `dtctl exec profiling`: resolve the window, run the analysis,
// read a view, and explain a failure or an empty result.
func Example() {
	// A stand-in for an environment, answering with a two-frame call tree.
	env := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"dataRootNodeId":"1","dataNodes":[
			{"id":"1","className":"app.Main","methodName":"main","apiInfo":{"systemApi":true},"samples":{"RUNNING":10},"childIds":["2"]},
			{"id":"2","className":"lib.Json","methodName":"parse","apiInfo":{"systemApi":false},"samples":{"RUNNING":8},"childIds":[]}]}`)
	}))
	defer env.Close()

	// The client comes from a dtctl configuration file, as it does for the
	// command. Here the file is a temporary one whose only context points at
	// the stand-in; see exampleClient.
	dir, err := os.MkdirTemp("", "profiling-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(exampleConfig(env.URL)), 0o600); err != nil {
		fmt.Println(err)
		return
	}
	c, err := exampleClient(path)
	if err != nil {
		fmt.Println(err)
		return
	}

	// A window over profiling.MaxWindow is refused, not shortened. The error
	// carries the request, so the caller decides how to narrow it.
	now := time.Now()
	window, err := profiling.ResolveWindow("now()-6h", "now()", now)
	var tooLong *profiling.WindowTooLongError
	if errors.As(err, &tooLong) {
		fmt.Println("window too long, using its last 2h")
		window = tooLong.Requested
		window.From = window.To - profiling.MaxWindow.Milliseconds()
	} else if err != nil {
		fmt.Println(err)
		return
	}

	analysis, err := profiling.NewHandler(c).Hotspots(context.Background(), profiling.HotspotsRequest{
		Entity: "PROCESS-0000000000000001",
		Window: window,
	})
	if err != nil {
		if f, ok := profiling.ClassifyError(err); ok {
			fmt.Println(f.Reason, f.Message)
			return
		}
		fmt.Println(err)
		return
	}

	fmt.Printf("%s over %s, after %d polls\n", analysis.Status, window.Span(), analysis.PollCount)
	stacks := analysis.Stacks(false)
	fmt.Printf("%d running samples\n", stacks.Samples)
	for _, row := range stacks.Rows {
		fmt.Printf("%2d %s\n", row.Running, row.Stack)
	}

	// A view without rows says why.
	fns, err := analysis.Functions(false, profiling.StateLock, profiling.SortBySelf)
	if err != nil {
		fmt.Println(err)
		return
	}
	if fns.Empty != "" {
		fmt.Println("no functions:", fns.Empty)
	}

	// Output:
	// window too long, using its last 2h
	// completed over 2h0m0s, after 0 polls
	// 10 running samples
	//  8 app.Main.main;lib.Json.parse
	//  2 app.Main.main
	// no functions: no_state_samples
}

// exampleClient builds the client a program hands to profiling.NewHandler,
// from the dtctl configuration file at path. A program uses sdk/session for
// this, under its own User-Agent; pkg/config and pkg/client are dtctl's own
// and send dtctl's.
//
// The example's file carries its token inline, so the configuration is sealed
// and no credential store of the host is asked for one. A program that works
// with the user's own login (`dtctl auth login`) loads the configuration with
// session.Load and does not seal it.
func exampleClient(path string) (*session.Client, error) {
	cfg, err := session.LoadFrom(path)
	if err != nil {
		return nil, err
	}
	cfg.SealInlineCredentials()
	return session.NewClientFromConfig(cfg, session.WithUserAgentProduct("profiling-example", "0.1.0"))
}

// exampleConfig is a dtctl configuration with one context for environment.
func exampleConfig(environment string) string {
	return fmt.Sprintf(`current-context: example
contexts:
  - name: example
    context:
      environment: %s
      token-ref: example
tokens:
  - name: example
    token: dt0c01.EXAMPLE
`, environment)
}
