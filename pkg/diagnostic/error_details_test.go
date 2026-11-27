package diagnostic

import (
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// A diagnostic.Error with only Err set shows the wrapped APIError's body, which
// APIError.Error() leaves out.
func TestErrorShowsWrappedAPIErrorDetails(t *testing.T) {
	e := &Error{Operation: "get x", StatusCode: 400, Err: httpclient.NewAPIError(400, "Bad", "field y is required")}
	if got := e.Error(); !strings.Contains(got, "API error (400): Bad - field y is required") {
		t.Errorf("Error() = %q", got)
	}
}
