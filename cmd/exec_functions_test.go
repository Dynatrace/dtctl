package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/vfs"
)

const execFunctionTestCode = "export default async function() { return 1 }"

// functionExecutorServer records every request body sent to it and answers
// each with a successful ad-hoc execution.
type functionExecutorServer struct {
	mu     sync.Mutex
	bodies []string
}

func (s *functionExecutorServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// execFunctionContext points dtctl at a recording server with a safety level
// that admits ad-hoc code (gated as a delete).
func execFunctionContext(t *testing.T) *functionExecutorServer {
	t.Helper()
	rec := &functionExecutorServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, r.Method+" "+r.URL.Path+" "+string(body))
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":1,"logs":""}`))
	}))
	t.Cleanup(srv.Close)
	isolatedConfig(t, safetyLevelConfig(srv.URL, "dangerously-unrestricted"))
	return rec
}

func runExecFunction(t *testing.T, argv []string, stdin string, files map[string][]byte) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts := RunOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(stdin)}
	if files != nil {
		opts.FS = vfs.NewMapFS(files)
	}
	code := Run(append([]string{"exec", "function"}, argv...), opts)
	return code, stdout.String() + stderr.String()
}

const executionsPath = "POST /platform/app-engine/function-executor/v1/executions "

// TestExecFunctionWithoutSDKMajorVersionsSendsTodaysBody pins the request body
// of an invocation without the flag, byte for byte, to what it was before the
// flag existed.
func TestExecFunctionWithoutSDKMajorVersionsSendsTodaysBody(t *testing.T) {
	rec := execFunctionContext(t)

	code, out := runExecFunction(t, []string{"--code", execFunctionTestCode, "--payload", `{"a":1}`}, "", nil)

	require.Zero(t, code, out)
	require.Equal(t, []string{
		executionsPath + `{"sourceCode":"export default async function() { return 1 }","payload":"{\"a\":1}"}`,
	}, rec.requests())
}

func TestExecFunctionSendsSDKMajorVersions(t *testing.T) {
	const versions = `{"@dynatrace-sdk/adhoc-utils":"1","@dynatrace-sdk/client-query":"2"}`
	const want = executionsPath + `{"sourceCode":"export default async function() { return 1 }",` +
		`"sdkMajorVersions":{"@dynatrace-sdk/adhoc-utils":"1","@dynatrace-sdk/client-query":"2"}}`

	tests := []struct {
		name  string
		argv  []string
		stdin string
		files map[string][]byte
		want  string
	}{
		{
			name: "inline",
			argv: []string{"--code", execFunctionTestCode, "--sdk-major-versions", versions},
			want: want,
		},
		{
			name:  "@file",
			argv:  []string{"--code", execFunctionTestCode, "--sdk-major-versions", "@versions.json"},
			files: map[string][]byte{"versions.json": []byte(versions + "\n")},
			want:  want,
		},
		{
			name:  "@- reads stdin",
			argv:  []string{"--code", execFunctionTestCode, "--sdk-major-versions", "@-"},
			stdin: versions,
			want:  want,
		},
		{
			name:  "with -f",
			argv:  []string{"-f", "script.js", "--sdk-major-versions", versions},
			files: map[string][]byte{"script.js": []byte(execFunctionTestCode)},
			want:  want,
		},
		{
			name: "--defer and --method are ignored for ad-hoc code, as without the flag",
			argv: []string{"--code", execFunctionTestCode, "--defer", "--method", "POST", "--sdk-major-versions", versions},
			want: want,
		},
		{
			name: "empty object is sent, not dropped",
			argv: []string{"--code", execFunctionTestCode, "--sdk-major-versions", "{}"},
			want: executionsPath + `{"sourceCode":"export default async function() { return 1 }","sdkMajorVersions":{}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := execFunctionContext(t)

			code, out := runExecFunction(t, tt.argv, tt.stdin, tt.files)

			require.Zero(t, code, out)
			require.Equal(t, []string{tt.want}, rec.requests())
		})
	}
}

// Every rejection happens before a request is sent.
func TestExecFunctionRejectsBadSDKMajorVersions(t *testing.T) {
	code := []string{"--code", execFunctionTestCode}
	tests := []struct {
		name    string
		argv    []string
		stdin   string
		files   map[string][]byte
		wantErr string
	}{
		{name: "malformed JSON", argv: append(code, "--sdk-major-versions", `{"a":`), wantErr: "invalid JSON"},
		{name: "empty value", argv: append(code, "--sdk-major-versions", ""), wantErr: "must not be empty"},
		{name: "blank value", argv: append(code, "--sdk-major-versions", "  "), wantErr: "must not be empty"},
		{name: "bare - for stdin", argv: append(code, "--sdk-major-versions", "-"), stdin: `{}`, wantErr: "pass --sdk-major-versions @- to read"},
		{
			name:    "repeated",
			argv:    append(code, "--sdk-major-versions", `{"a":"1"}`, "--sdk-major-versions", `{"b":"2"}`),
			wantErr: "flag can only be provided once",
		},
		{name: "array", argv: append(code, "--sdk-major-versions", `["1"]`), wantErr: "not an object"},
		{name: "number", argv: append(code, "--sdk-major-versions", `1`), wantErr: "not an object"},
		{name: "string", argv: append(code, "--sdk-major-versions", `"1"`), wantErr: "not an object"},
		{name: "null", argv: append(code, "--sdk-major-versions", `null`), wantErr: "got null"},
		{name: "nested object value", argv: append(code, "--sdk-major-versions", `{"a":{"b":"1"}}`), wantErr: `value for "a"`},
		{name: "number value", argv: append(code, "--sdk-major-versions", `{"a":1}`), wantErr: `value for "a" is 1, not a string`},
		{name: "null value", argv: append(code, "--sdk-major-versions", `{"a":null}`), wantErr: `value for "a" is null`},
		{name: "@ without a name", argv: append(code, "--sdk-major-versions", "@"), wantErr: "needs a filename"},
		{
			name:    "missing file",
			argv:    append(code, "--sdk-major-versions", "@missing.json"),
			files:   map[string][]byte{},
			wantErr: "reading --sdk-major-versions from @missing.json",
		},
		{
			name:    "-f - with @-",
			argv:    []string{"-f", "-", "--sdk-major-versions", "@-"},
			stdin:   execFunctionTestCode,
			wantErr: "--sdk-major-versions @- and -f - both read stdin",
		},
		{
			name:    "--data - with @-",
			argv:    append(code, "--data", "-", "--sdk-major-versions", "@-"),
			stdin:   `{}`,
			wantErr: "--sdk-major-versions @- and --data - both read stdin",
		},
		{
			name:    "named function",
			argv:    []string{"myapp/myfunction", "--sdk-major-versions", `{"a":"1"}`},
			wantErr: "only supported for ad-hoc execution with --code or -f",
		},
		{
			name:    "named function with POST",
			argv:    []string{"myapp/myfunction", "--method", "POST", "--sdk-major-versions", `{"a":"1"}`},
			wantErr: "only supported for ad-hoc execution with --code or -f",
		},
		{
			name:    "--defer",
			argv:    []string{"myapp/myfunction", "--defer", "--sdk-major-versions", `{"a":"1"}`},
			wantErr: "only supported for ad-hoc execution with --code or -f",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := execFunctionContext(t)

			exit, out := runExecFunction(t, tt.argv, tt.stdin, tt.files)

			require.NotZero(t, exit, out)
			require.Contains(t, out, tt.wantErr)
			require.Contains(t, out, "--sdk-major-versions")
			require.Empty(t, rec.requests(), "no request may be sent")
		})
	}
}

// Every rejection of the value itself exits with the usage code. (A repeated
// flag is refused by the flag parser, like exec workflow --input, and keeps
// that path's exit code.)
func TestExecFunctionSDKMajorVersionsExitsWithUsageCode(t *testing.T) {
	code := []string{"--code", execFunctionTestCode}
	for name, argv := range map[string][]string{
		"malformed":      append(code, "--sdk-major-versions", `{"a":`),
		"empty":          append(code, "--sdk-major-versions", ""),
		"missing file":   append(code, "--sdk-major-versions", "@missing.json"),
		"named function": {"myapp/myfunction", "--sdk-major-versions", `{"a":"1"}`},
	} {
		t.Run(name, func(t *testing.T) {
			execFunctionContext(t)
			exit, out := runExecFunction(t, argv, "", map[string][]byte{})
			require.Equal(t, 2, exit, out)
		})
	}
}

// With several bad values the first one in the document is named, on every
// run: a Go map would name a random one.
func TestExecFunctionSDKMajorVersionsErrorIsDeterministic(t *testing.T) {
	execFunctionContext(t)
	argv := []string{"--code", execFunctionTestCode, "--sdk-major-versions", `{"a":"1","d":4,"b":5,"c":6}`}
	for range 10 {
		_, out := runExecFunction(t, argv, "", nil)
		require.Contains(t, out, `the value for "d" is 4, not a string`)
	}
}

// A malformed value names the flag and shows a usable example.
func TestExecFunctionSDKMajorVersionsErrorShowsExample(t *testing.T) {
	rec := execFunctionContext(t)

	exit, out := runExecFunction(t, []string{"--code", execFunctionTestCode, "--sdk-major-versions", "[]"}, "", nil)

	require.NotZero(t, exit)
	require.Contains(t, out, `e.g. '{"@dynatrace-sdk/adhoc-utils":"1"}'`)
	require.Empty(t, rec.requests())
}

// @- on a terminal fails fast with advice spelled the way this flag reads
// stdin: "--sdk-major-versions -" would be parsed as inline JSON.
func TestReadSDKMajorVersionsTerminalStdin(t *testing.T) {
	_, err := readSDKMajorVersions(context.Background(), "@-", queryStdin{r: strings.NewReader(""), isTerminal: true})

	require.Error(t, err)
	require.Contains(t, err.Error(), "--sdk-major-versions @- reads from stdin, but stdin is a terminal")
	require.NotContains(t, err.Error(), "--sdk-major-versions -)")
	var invalid *invalidInputError
	require.ErrorAs(t, err, &invalid)
}

// Every way of getting the value wrong reports the same agent code, whatever
// the message says (a path like @config.json must not read as config_error).
func TestExecFunctionSDKMajorVersionsAgentErrorCode(t *testing.T) {
	code := []string{"--code", execFunctionTestCode}
	cases := map[string][]string{
		"malformed JSON":   append(code, "--sdk-major-versions", `{"a":`),
		"array":            append(code, "--sdk-major-versions", `[]`),
		"null":             append(code, "--sdk-major-versions", `null`),
		"number value":     append(code, "--sdk-major-versions", `{"a":1}`),
		"@ without a name": append(code, "--sdk-major-versions", "@"),
		"missing file":     append(code, "--sdk-major-versions", "@config.json"),
		"stdin clash":      {"-f", "-", "--sdk-major-versions", "@-"},
		"named function":   {"myapp/myfunction", "--sdk-major-versions", `{"a":"1"}`},
		"named --defer":    {"myapp/myfunction", "--defer", "--sdk-major-versions", `{"a":"1"}`},
		"empty":            append(code, "--sdk-major-versions", ""),
		"bare -":           append(code, "--sdk-major-versions", "-"),
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			rec := execFunctionContext(t)
			var stdout bytes.Buffer
			exit := Run(append(append([]string{"exec", "function"}, argv...), "--agent"),
				RunOptions{Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader(execFunctionTestCode), FS: vfs.NewMapFS(nil)})

			require.NotZero(t, exit)
			var env map[string]any
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &env), stdout.String())
			require.Equal(t, "validation_error", errorCode(t, env))
			require.Empty(t, rec.requests())
		})
	}
}

// Without --code or -f the mode error comes first: it is the relevant one, and
// a value that will never be used is not read first.
func TestExecFunctionSDKMajorVersionsModeCheckedFirst(t *testing.T) {
	rec := execFunctionContext(t)

	exit, out := runExecFunction(t, []string{"myapp/myfunction", "--sdk-major-versions", `{bad`}, "", nil)
	require.NotZero(t, exit)
	require.Contains(t, out, "only supported for ad-hoc execution")
	require.NotContains(t, out, "invalid JSON")

	// Empty stdin: had @- been read first, this would fail with "no input
	// arrived on stdin" instead.
	exit, out = runExecFunction(t, []string{"myapp/myfunction", "--sdk-major-versions", "@-"}, "", nil)
	require.NotZero(t, exit)
	require.Contains(t, out, "only supported for ad-hoc execution")
	require.NotContains(t, out, "stdin")
	require.Empty(t, rec.requests())
}
