package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// TestParseAgreesWithExecute holds Parse to what Execute does, on every
// command line TestFileFlagsAreDeclared probes: each file Execute reads is in
// InputFiles, a command line Execute ran far enough to read a file is not
// refused, and a command that read stdin is reported as RequiresInput.
func TestParseAgreesWithExecute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":400,"message":"parse probe"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	isolateProbeHost(t)

	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	probes := fileFlagProbes(root)
	require.NotEmpty(t, probes)

	var mu sync.Mutex
	var mismatches []string
	report := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		mismatches = append(mismatches, fmt.Sprintf(format, args...))
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, p := range probes {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fsys := &recordingFS{}
			stdin := &recordingReader{r: strings.NewReader("fetch logs")}
			runProbe(srv.URL, p.argv, fsys, stdin)
			var read []string
			for _, r := range fsys.reads {
				if _, _, ok := p.attribute(r.name); ok {
					read = append(read, vfs.Key(r.name))
				}
			}

			res, err := Parse(p.argv, ParseOptions{MinStability: config.StabilityExperimental})
			var pe *ParseError
			switch {
			case errors.As(err, &pe):
				if len(read) > 0 {
					report("%v: Parse refused it (%s: %s), but Execute read %v", p.argv, pe.Code, pe.Message, read)
				}
				return
			case err != nil:
				report("%v: Parse failed: %v", p.argv, err)
				return
			}
			for _, name := range read {
				if !slices.Contains(res.InputFiles, name) {
					report("%v: Execute read %s, but InputFiles is %v", p.argv, name, res.InputFiles)
				}
			}
			if stdin.read.Load() && !res.RequiresInput {
				report("%v: Execute read stdin, but RequiresInput is false", p.argv)
			}
		}()
	}
	wg.Wait()
	slices.Sort(mismatches)
	for _, m := range slices.Compact(mismatches) {
		t.Error(m)
	}
}

// TestParseUsageErrorsMatchExecute holds a refused command line to the error
// Execute reports for it: the --agent envelope's code and message, and the exit
// code.
func TestParseUsageErrorsMatchExecute(t *testing.T) {
	blocked := map[string]string{"config": "it manages the local config file"}
	for name, argv := range map[string][]string{
		"unknown command":    {"bogusverb"},
		"unknown flag":       {"get", "workflows", "--bogus-flag"},
		"required flag":      {"apply"},
		"argument count":     {"describe", "workflow"},
		"flag group":         {"commands", "--brief", "--full"},
		"empty flag value":   {"query", "--file="},
		"no dry run":         {"get", "workflows", "--dry-run"},
		"blocked command":    {"config", "view"},
		"below the floor":    {"get", "breakpoints"},
		"blocked subcommand": {"config", "get-contexts", "--output", "json"},
		"missing resource":   {"get"},
		"unknown resource":   {"get", "bogusresource"},
		"not a resource":     {"describe", "field", "x"},
	} {
		t.Run(name, func(t *testing.T) {
			argv := append(append([]string{}, argv...), "--agent")
			var stdout, stderr bytes.Buffer
			code := Run(argv, RunOptions{
				Concurrent:      true,
				Session:         &Session{EnvironmentURL: "https://env.example.test", Token: "dt0c01.T.T"},
				BlockedCommands: blocked,
				Stdout:          &stdout,
				Stderr:          &stderr,
			})
			var envelope struct {
				Error struct {
					Code, Message string
					Suggestions   []string
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope), "Execute stdout: %s", stdout.String())

			_, err := Parse(argv, ParseOptions{BlockedCommands: blocked})
			var pe *ParseError
			require.ErrorAs(t, err, &pe, "Execute refused %v; Parse must too", argv)
			assert.Equal(t, envelope.Error.Code, pe.Code)
			assert.Equal(t, envelope.Error.Message, pe.Message)
			assert.Equal(t, envelope.Error.Suggestions, pe.Suggestions)
			assert.Equal(t, code, pe.ExitCode)
		})
	}
}

// TestParseHasNoSideEffects holds Parse to reading nothing: on every probed
// command line it reads no file, no stdin, and leaves the process environment
// as it was. It cannot call the platform: a parse-only session has no
// environment, and newClientFromConfig refuses it.
func TestParseHasNoSideEffects(t *testing.T) {
	isolateProbeHost(t)
	root := newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root
	probes := fileFlagProbes(root)
	env := os.Environ()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var mu sync.Mutex
	var touched []string
	for _, p := range probes {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fsys := &recordingFS{}
			stdin := &recordingReader{r: strings.NewReader("fetch logs")}
			_, _ = parseWith(p.argv, ParseOptions{MinStability: config.StabilityExperimental}, fsys, stdin)
			if len(fsys.reads) > 0 || stdin.read.Load() {
				mu.Lock()
				touched = append(touched, fmt.Sprintf("%v: read files %v, stdin %v", p.argv, fsys.reads, stdin.read.Load()))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, s := range touched {
		t.Error(s)
	}
	assert.Equal(t, env, os.Environ(), "Parse changed the process environment")
}

// TestParseRefusesThePlatform holds the parse-only session to failing closed:
// a client built under it is an error, never one built from host credentials.
func TestParseRefusesThePlatform(t *testing.T) {
	ctx := withInvocation(context.Background(), &invocation{session: &Session{parseOnly: true}, concurrent: true})
	_, err := newClientFromConfig(ctx, &config.Config{})
	require.ErrorIs(t, err, errParseOnlySession)
}

// TestParseConcurrent runs Parse in parallel with itself and with concurrent
// Run invocations; run under -race it holds that they share nothing.
func TestParseConcurrent(t *testing.T) {
	lines := [][]string{
		{"apply", "-f", "d.yaml", "--write-id"},
		{"query"},
		{"exec", "api", "/platform/x", "-X", "POST", "-d", "@body.json"},
		{"get", "workflows", "-o", "json"},
	}
	want := make([]*ParseResult, len(lines))
	for i, argv := range lines {
		res, err := Parse(argv, ParseOptions{})
		require.NoError(t, err)
		want[i] = res
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			res, err := Parse(lines[i%len(lines)], ParseOptions{})
			if assert.NoError(t, err) {
				assert.Equal(t, want[i%len(lines)], res)
			}
		}()
		go func() {
			defer wg.Done()
			var stdout, stderr bytes.Buffer
			Run([]string{"version", "--agent"}, RunOptions{
				Concurrent: true,
				Session:    &Session{EnvironmentURL: "https://env.example.test", Token: "dt0c01.T.T"},
				Stdout:     &stdout,
				Stderr:     &stderr,
			})
		}()
	}
	wg.Wait()
}
