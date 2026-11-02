package appengine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func newTestFunctionHandler(t *testing.T, status int, body string) *FunctionHandler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, time.Millisecond, time.Millisecond))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return NewFunctionHandler(c)
}

// App Engine answers 540/541 when the submitted code failed. Both entry points
// must report that as an ExecutionError, not as the httpclient.APIError that
// CheckResponse would build for a 5xx.
func TestFunctionHandler_ExecutionErrorStatuses(t *testing.T) {
	const body = `{"error":"ReferenceError: x is not defined"}`
	calls := map[string]func(*FunctionHandler) error{
		"InvokeFunction": func(h *FunctionHandler) error {
			_, err := h.InvokeFunction(context.Background(), &FunctionInvokeRequest{
				Method: "POST", AppID: "my-app", FunctionName: "fn",
			})
			return err
		},
		"ExecuteCode": func(h *FunctionHandler) error {
			_, err := h.ExecuteCode(context.Background(), "export default () => x", "")
			return err
		},
	}
	statuses := []struct {
		status  int
		message string
	}{
		{statusJSError, "JavaScript error occurred"},
		{statusSyntaxError, "runtime error occurred"},
	}

	for callName, call := range calls {
		for _, st := range statuses {
			t.Run(callName+"/"+strconv.Itoa(st.status), func(t *testing.T) {
				err := call(newTestFunctionHandler(t, st.status, body))

				var execErr *ExecutionError
				if !errors.As(err, &execErr) {
					t.Fatalf("error = %T %v, want *ExecutionError", err, err)
				}
				if execErr.StatusCode != st.status {
					t.Errorf("StatusCode = %d, want %d", execErr.StatusCode, st.status)
				}
				if execErr.Message != st.message {
					t.Errorf("Message = %q, want %q", execErr.Message, st.message)
				}
				if execErr.Body != body {
					t.Errorf("Body = %q, want %q", execErr.Body, body)
				}
				var apiErr *httpclient.APIError
				if errors.As(err, &apiErr) {
					t.Errorf("error also matches *httpclient.APIError (status %d)", apiErr.StatusCode)
				}
			})
		}
	}
}
