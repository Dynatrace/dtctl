package profiling_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/profiling"
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
	c, err := client.NewForTesting(env.URL, "token")
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

	analysis, err := profiling.NewHandler(c).Analyze(context.Background(), profiling.Payload{
		Kind:     profiling.Kind["hotspots"],
		EntityID: "PROCESS-0000000000000001",
		From:     window.From,
		To:       window.To,
	})
	if err != nil {
		if f, ok := profiling.ClassifyError(err); ok {
			fmt.Println(f.Reason, f.Advice)
			return
		}
		fmt.Println(err)
		return
	}

	stacks := analysis.Stacks(false)
	fmt.Printf("%d running samples\n", stacks.Samples)
	for _, row := range stacks.Rows {
		fmt.Printf("%2d %s\n", row.Running, row.Stack)
	}

	// A view without rows says why.
	if fns := analysis.Functions(false, "lock", "self"); fns == nil {
		reason, note := analysis.WhyEmpty(false, "lock")
		fmt.Println(reason)
		fmt.Println(note)
	}

	// Output:
	// window too long, using its last 2h
	// 10 running samples
	//  8 app.Main.main;lib.Json.parse
	//  2 app.Main.main
	// no_state_samples
	// no lock samples for PROCESS-0000000000000001 in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting
}
