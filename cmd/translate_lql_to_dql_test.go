package cmd

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

const lqlToDqlEndpoint = "/platform/openpipeline/v1/matcher/lqlToDql"

const lqlToDqlResponse = `{"query":"matchesValue(log.source, \"snmptraps\")"}`

func setupLqlToDqlServer(t *testing.T, responseBody string) (*testutil.MockServer, string, func()) {
	t.Helper()
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		lqlToDqlEndpoint: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(responseBody))
		},
	})
	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	return ms, configPath, func() {
		ms.Close()
		cleanup()
	}
}

func withLqlToDqlGlobals(t *testing.T, configPath string, fn func()) {
	t.Helper()
	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origAgent := agentMode(context.Background())
	origFormat := outputFormat(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.agentMode = origAgent
		gFlags.outputFormat = origFormat
	}()
	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.agentMode = false
	gFlags.outputFormat = ""
	fn()
}

func TestTranslateLqlToDqlCmd_BothArgAndFile(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)
		_ = translateLqlToDqlCmd.Flags().Set("file", "some.lql")

		err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{"log.source=\"snmptraps\""})
		if err == nil {
			t.Fatal("expected error when both arg and --file are given")
		}
		if !strings.Contains(err.Error(), "not both") {
			t.Errorf("error = %q, want mention of 'not both'", err.Error())
		}
	})
}

func TestTranslateLqlToDqlCmd_NeitherArgNorFile(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)

		err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{})
		if err == nil {
			t.Fatal("expected error when neither arg nor --file is given")
		}
		if !strings.Contains(err.Error(), "--file") {
			t.Errorf("error = %q, want mention of '--file'", err.Error())
		}
	})
}

func TestTranslateLqlToDqlCmd_InlineArg(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)

		out := captureStdout(t, func() {
			if err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{`log.source="snmptraps"`}); err != nil {
				t.Fatalf("RunE() error = %v", err)
			}
		})

		if !strings.Contains(out, "matchesValue") {
			t.Errorf("output missing translated DQL; got: %q", out)
		}
	})
}

func TestTranslateLqlToDqlCmd_FileArg(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	lqlFile := testutil.CreateTempFile(t, `log.source="snmptraps"`, "*.lql")

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)
		_ = translateLqlToDqlCmd.Flags().Set("file", lqlFile)

		out := captureStdout(t, func() {
			if err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{}); err != nil {
				t.Fatalf("RunE() error = %v", err)
			}
		})

		if !strings.Contains(out, "matchesValue") {
			t.Errorf("output missing translated DQL; got: %q", out)
		}
	})
}

func TestTranslateLqlToDqlCmd_FileArgMissing(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)
		_ = translateLqlToDqlCmd.Flags().Set("file", "/nonexistent/path.lql")

		err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{})
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}

func TestTranslateLqlToDqlCmd_StdinArg(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	// Redirect stdin to a pipe with LQL content.
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	_, _ = w.WriteString(`log.source="snmptraps"`)
	_ = w.Close()
	defer func() { os.Stdin = origStdin }()

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)
		_ = translateLqlToDqlCmd.Flags().Set("file", "-")

		out := captureStdout(t, func() {
			if err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{}); err != nil {
				t.Fatalf("RunE() error = %v", err)
			}
		})

		if !strings.Contains(out, "matchesValue") {
			t.Errorf("output missing translated DQL; got: %q", out)
		}
	})
}

func TestTranslateLqlToDqlCmd_ExplicitOutputFormat(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	withLqlToDqlGlobals(t, configPath, func() {
		gFlags.outputFormat = "json"

		testutil.ResetCommandFlags(translateLqlToDqlCmd)

		// Mark the output flag as changed to simulate an explicit -o json.
		rootCmd.PersistentFlags().Lookup("output").Changed = true
		defer func() {
			rootCmd.PersistentFlags().Lookup("output").Changed = false
		}()

		out := captureStdout(t, func() {
			if err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{`log.source="snmptraps"`}); err != nil {
				t.Fatalf("RunE() error = %v", err)
			}
		})

		if !strings.Contains(out, "matchesValue") {
			t.Errorf("json output missing DQL; got: %q", out)
		}
	})
}

func TestTranslateLqlToDqlCmd_AgentMode(t *testing.T) {
	_, configPath, cleanup := setupLqlToDqlServer(t, lqlToDqlResponse)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origAgent := agentMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.agentMode = origAgent
	}()
	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.agentMode = true

	testutil.ResetCommandFlags(translateLqlToDqlCmd)

	out := captureStdout(t, func() {
		if err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{`log.source="snmptraps"`}); err != nil {
			t.Fatalf("RunE() error = %v", err)
		}
	})

	for _, want := range []string{`"ok":true`, "matchesValue", `"suggestions"`} {
		if !strings.Contains(out, want) {
			t.Errorf("agent envelope missing %q; got:\n%s", want, out)
		}
	}
}

func TestTranslateLqlToDqlCmd_APIError(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		lqlToDqlEndpoint: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"invalid LQL"}}`))
		},
	})
	defer ms.Close()
	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	withLqlToDqlGlobals(t, configPath, func() {
		testutil.ResetCommandFlags(translateLqlToDqlCmd)

		err := translateLqlToDqlCmd.RunE(translateLqlToDqlCmd, []string{`INVALID`})
		if err == nil {
			t.Fatal("expected error for 400 API response")
		}
	})
}
