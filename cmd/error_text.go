package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/resources/workflow"
	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// errorText renders err for a user or an agent. httpclient.APIError keeps the
// upstream response body out of Error() (it sits in Details), so the CLI reads
// it explicitly: the body is where the server names the failing field, the
// missing scope or the errorRef support asks for.
//
// A diagnostic.Error owns its own rendering (operation, advice) and is left
// alone; the handlers that build one from an APIError add the details
// themselves.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	var diagErr *diagnostic.Error
	if errors.As(err, &diagErr) {
		return err.Error()
	}
	// A DQL error the API did not answer in its envelope keeps the capped
	// body in Details, outside Error().
	var qErr *sdkquery.QueryError
	if errors.As(err, &qErr) && qErr.Details != "" {
		return err.Error() + " - " + qErr.Details
	}
	return httpclient.ErrorText(err)
}

// taskLogErrorText renders a workflow.TaskLogError like its Error() but with
// each task's own error through errorText, so the per-task response details
// (which Error() leaves out) are kept.
func taskLogErrorText(e *workflow.TaskLogError) string {
	var b strings.Builder
	fmt.Fprintf(&b, "could not fetch the log of %d of %d tasks in execution %s:", len(e.Failed), e.Total, e.ExecutionID)
	for _, f := range e.Failed {
		fmt.Fprintf(&b, "\n  %s: %s", f.Task, errorText(f.Err))
	}
	return b.String()
}
