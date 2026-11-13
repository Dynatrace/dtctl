package appengine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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

func TestGetSDKVersions_NewStructure(t *testing.T) {
	h := newTestFunctionHandler(t, 200, `{"layerVersion":"2026","packages":{"@x/a":["1.0.4"],"@x/b":["3.4.1","4.0.1"]}}`)
	got, err := h.GetSDKVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LayerVersion != "2026" || len(got.Packages) != 2 || len(got.Packages["@x/b"]) != 2 {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestGetSDKVersions_UnexpectedStructure(t *testing.T) {
	for _, body := range []string{`{"versions":[{"version":"1","default":true}]}`, `{"packages":["a"]}`} {
		h := newTestFunctionHandler(t, 200, body)
		_, err := h.GetSDKVersions(context.Background())
		if err == nil || !strings.Contains(err.Error(), "unexpected structure") {
			t.Errorf("body %s: want unexpected structure error, got %v", body, err)
		}
	}
}

// ExecuteCode keeps sending exactly the body it always did; ExecuteRequest
// omits sdkMajorVersions when nil and sends an empty map as {}.
func TestFunctionHandler_ExecuteRequestBody(t *testing.T) {
	versions := map[string]string{"@dynatrace-sdk/adhoc-utils": "1"}
	empty := map[string]string{}
	tests := []struct {
		name string
		call func(*FunctionHandler) error
		want string
	}{
		{
			name: "ExecuteCode",
			call: func(h *FunctionHandler) error {
				_, err := h.ExecuteCode(context.Background(), "code", "p")
				return err
			},
			want: `{"sourceCode":"code","payload":"p"}`,
		},
		{
			name: "nil versions",
			call: func(h *FunctionHandler) error {
				_, err := h.ExecuteRequest(context.Background(), &FunctionExecutorRequest{SourceCode: "code"})
				return err
			},
			want: `{"sourceCode":"code"}`,
		},
		{
			name: "empty versions",
			call: func(h *FunctionHandler) error {
				_, err := h.ExecuteRequest(context.Background(), &FunctionExecutorRequest{SourceCode: "code", SDKMajorVersions: empty})
				return err
			},
			want: `{"sourceCode":"code","sdkMajorVersions":{}}`,
		},
		{
			name: "versions",
			call: func(h *FunctionHandler) error {
				_, err := h.ExecuteRequest(context.Background(), &FunctionExecutorRequest{SourceCode: "code", SDKMajorVersions: versions})
				return err
			},
			want: `{"sourceCode":"code","sdkMajorVersions":{"@dynatrace-sdk/adhoc-utils":"1"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got = string(b)
				_, _ = w.Write([]byte(`{"result":1,"logs":""}`))
			}))
			t.Cleanup(srv.Close)
			c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, time.Millisecond, time.Millisecond))
			if err != nil {
				t.Fatalf("httpclient.New: %v", err)
			}
			if err := tt.call(NewFunctionHandler(c)); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got != tt.want {
				t.Errorf("body = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetRuntimeTypeDefinitions(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/app-engine/function-executor/v1/runtime-type-definitions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"packageName":"@dynatrace-sdk/client-automation","version":"8.13.0","fileName":"f.d.ts","typeDefinitions":"declare module 'x' {}"}`))
	}))
	defer srv.Close()
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, time.Millisecond, time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	got, err := NewFunctionHandler(c).GetRuntimeTypeDefinitions(context.Background(), "@dynatrace-sdk/client-automation", 8)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("packageName") != "@dynatrace-sdk/client-automation" || gotQuery.Get("majorVersion") != "8" {
		t.Errorf("unexpected query: %v", gotQuery)
	}
	if got.Version != "8.13.0" || got.FileName != "f.d.ts" || got.TypeDefinitions == "" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestGetRuntimeTypeDefinitions_Errors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"not found", 404, `{"error":{"code":404,"message":"The package or the requested major version is not available in the runtime"}}`, "not available in the runtime"},
		{"bad request", 400, `{"error":{"code":400,"message":"Invalid package name or major version"}}`, "Invalid package name"},
		{"unexpected structure", 200, `{"foo":"bar"}`, "unexpected structure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestFunctionHandler(t, tc.status, tc.body)
			_, err := h.GetRuntimeTypeDefinitions(context.Background(), "@x/y", 1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
