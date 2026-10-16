package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func newPlatformMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/platform/management/v1/environment", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"environmentId": "test-env-123",
			"type":          "INTERNAL",
			"state":         "ACTIVE",
			"createTime":    "2025-03-13T13:11:04Z",
			"blockTime":     "2031-01-01T00:00:00Z",
		})
	})

	mux.HandleFunc("/platform/management/v1/environment/license/settings", func(w http.ResponseWriter, r *http.Request) {
		all := []map[string]string{
			{"key": "AUTOMATION", "value": "true"},
			{"key": "AI_FUNCTIONS", "value": "true"},
		}
		var settings []map[string]string
		keys := r.URL.Query()["keys"]
		if len(keys) > 0 {
			for _, s := range all {
				for _, k := range keys {
					if s["key"] == k {
						settings = append(settings, s)
					}
				}
			}
		} else {
			settings = all
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
	})

	mux.HandleFunc("/platform/management/v1/environment/license", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trial":                false,
			"platformSubscription": true,
		})
	})

	return httptest.NewServer(mux)
}

func setupPlatformCmdTest(t *testing.T, srv *httptest.Server, format string) {
	t.Helper()
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	origCfgFile := cfgFile(context.Background())
	origFormat, origAgent := outputFormat(context.Background()), agentMode(context.Background())
	t.Cleanup(func() {
		gFlags.cfgFile = origCfgFile
		gFlags.outputFormat, gFlags.agentMode = origFormat, origAgent
	})
	gFlags.cfgFile = configPath
	gFlags.outputFormat = format
	gFlags.agentMode = false

	cfg := config.NewConfig()
	cfg.SetContext("test", srv.URL, "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
}

func capturePlatformStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func TestGetEnvironmentCmd(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "json")

	out := capturePlatformStdout(t, func() {
		if err := getEnvironmentCmd.RunE(getEnvironmentCmd, nil); err != nil {
			t.Fatalf("get environment: %v", err)
		}
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got["environmentId"] != "test-env-123" {
		t.Errorf("environmentId = %v, want test-env-123", got["environmentId"])
	}
	if got["state"] != "ACTIVE" {
		t.Errorf("state = %v, want ACTIVE", got["state"])
	}
}

func TestGetLicenseCmd(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "json")

	out := capturePlatformStdout(t, func() {
		if err := getLicenseCmd.RunE(getLicenseCmd, nil); err != nil {
			t.Fatalf("get license: %v", err)
		}
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got["trial"] != false {
		t.Errorf("trial = %v, want false", got["trial"])
	}
	if got["platformSubscription"] != true {
		t.Errorf("platformSubscription = %v, want true", got["platformSubscription"])
	}
}

func TestGetLicenseSettingsCmd_All(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "json")

	out := capturePlatformStdout(t, func() {
		if err := getLicenseSettingsCmd.RunE(getLicenseSettingsCmd, nil); err != nil {
			t.Fatalf("get license-settings: %v", err)
		}
	})

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON array: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Errorf("len(settings) = %d, want 2", len(got))
	}
}

func TestGetLicenseSettingsCmd_WithKey(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "json")

	out := capturePlatformStdout(t, func() {
		if err := getLicenseSettingsCmd.RunE(getLicenseSettingsCmd, []string{"AUTOMATION"}); err != nil {
			t.Fatalf("get license-settings AUTOMATION: %v", err)
		}
	})

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON array: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Errorf("len(settings) = %d, want 1", len(got))
	}
	if got[0]["key"] != "AUTOMATION" {
		t.Errorf("key = %v, want AUTOMATION", got[0]["key"])
	}
}

func TestPlatformCommandArgs(t *testing.T) {
	for _, cmd := range []*cobra.Command{
		getEnvironmentCmd,
		getLicenseCmd,
		describeEnvironmentCmd,
		describeLicenseCmd,
	} {
		if err := cmd.Args(cmd, []string{}); err != nil {
			t.Errorf("%s: expected no args to be accepted, got: %v", cmd.Use, err)
		}
		if err := cmd.Args(cmd, []string{"extra"}); err == nil {
			t.Errorf("%s: expected extra args to be rejected", cmd.Use)
		}
	}
}

func TestUsePlatformDescribeTextView(t *testing.T) {
	originalFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	defer func() { gFlags.outputFormat = originalFormat }()
	defer func() { gFlags.agentMode = originalAgentMode }()

	tests := []struct {
		name   string
		format string
		want   bool
	}{
		{name: "default", format: "", want: true},
		{name: "table", format: "table", want: true},
		{name: "wide", format: "wide", want: true},
		{name: "json", format: "json", want: false},
		{name: "yaml", format: "yaml", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.agentMode = false
			gFlags.outputFormat = tt.format
			if got := usePlatformDescribeTextView(context.Background()); got != tt.want {
				t.Fatalf("usePlatformDescribeTextView() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("agent mode forces structured view", func(t *testing.T) {
		// Restored: agentMode is package state, and leaving it on leaks into
		// every test that runs after this one in the package.
		prev := agentMode(context.Background())
		gFlags.agentMode = true
		t.Cleanup(func() { gFlags.agentMode = prev })
		gFlags.outputFormat = "table"
		if got := usePlatformDescribeTextView(context.Background()); got {
			t.Fatalf("usePlatformDescribeTextView() = %v, want false when agent mode enabled", got)
		}
	})
}

func TestDescribeEnvironmentCmd_ZeroTimestamp(t *testing.T) {
	// Verify that zero-value CreateTime and BlockTime are not rendered.
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Omit createTime and blockTime to produce zero time.Time values.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"environmentId": "zero-ts-env",
			"type":          "TRIAL",
			"state":         "ACTIVE",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "table")

	out := capturePlatformStdout(t, func() {
		if err := describeEnvironmentCmd.RunE(describeEnvironmentCmd, nil); err != nil {
			t.Fatalf("describe environment: %v", err)
		}
	})

	if strings.Contains(out, "0001-01-01") {
		t.Errorf("zero timestamp rendered in output, want it omitted:\n%s", out)
	}
}

func TestGetLicenseSettingsCmd_MultiKey(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "json")

	out := capturePlatformStdout(t, func() {
		if err := getLicenseSettingsCmd.RunE(getLicenseSettingsCmd, []string{"AUTOMATION", "AI_FUNCTIONS"}); err != nil {
			t.Fatalf("get license-settings AUTOMATION AI_FUNCTIONS: %v", err)
		}
	})

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON array: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Errorf("len(settings) = %d, want 2 (one per key)", len(got))
	}
}

func TestDescribeEnvironmentCmd_Table(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "table")

	out := capturePlatformStdout(t, func() {
		if err := describeEnvironmentCmd.RunE(describeEnvironmentCmd, nil); err != nil {
			t.Fatalf("describe environment: %v", err)
		}
	})

	if !strings.Contains(out, "test-env-123") {
		t.Errorf("expected environment ID in output, got:\n%s", out)
	}
	if !strings.Contains(out, "ACTIVE") {
		t.Errorf("expected state ACTIVE in output, got:\n%s", out)
	}
}

func TestDescribeLicenseCmd_Table(t *testing.T) {
	srv := newPlatformMockServer(t)
	defer srv.Close()
	setupPlatformCmdTest(t, srv, "table")

	out := capturePlatformStdout(t, func() {
		if err := describeLicenseCmd.RunE(describeLicenseCmd, nil); err != nil {
			t.Fatalf("describe license: %v", err)
		}
	})

	if !strings.Contains(out, "false") {
		t.Errorf("expected trial=false in output, got:\n%s", out)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("expected platformSubscription=true in output, got:\n%s", out)
	}
}
