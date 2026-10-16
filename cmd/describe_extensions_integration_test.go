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
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// describeExtensionMux serves the endpoints `describe extension` touches for the
// JSON/YAML path: the version list, the version details, and the (empty)
// monitoring-configuration list. The details payload uses the raw API field
// names (featureSetsDetails, metadata, ...) so the whole command path — HTTP →
// unmarshal → buildFeatureSetsOutput → printer — is exercised end to end.
func describeExtensionMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Extension with feature sets and metric metadata.
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.with-fs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":      []map[string]any{{"version": "1.2.3", "extensionName": "com.example.with-fs"}},
			"totalCount": 1,
		})
	})
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.with-fs/environment-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.2.3"})
	})
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.with-fs/1.2.3", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"extensionName": "com.example.with-fs",
			"version": "1.2.3",
			"featureSets": ["default", "advanced"],
			"featureSetsDetails": {
				"default": {
					"metrics": [
						{"key": "ext.uptime", "metadata": {"displayName": "Instance uptime", "unit": "Second"}},
						{"key": "ext.bare"}
					]
				},
				"advanced": {"metrics": []}
			}
		}`))
	})

	// Extension with no feature sets at all.
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.no-fs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":      []map[string]any{{"version": "1.0.0", "extensionName": "com.example.no-fs"}},
			"totalCount": 1,
		})
	})
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.no-fs/environment-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.0.0"})
	})
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.no-fs/1.0.0", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"extensionName": "com.example.no-fs", "version": "1.0.0"}`))
	})

	// Monitoring configurations: empty for every extension.
	mux.HandleFunc("/platform/extensions/v2/extensions/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": 0})
	})

	return mux
}

// runDescribeExtensionJSON drives the real describe extension command against
// srv and returns the parsed JSON output.
func runDescribeExtensionJSON(t *testing.T, srv *httptest.Server, name string, featureSetMetrics bool) map[string]any {
	t.Helper()

	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	origFormat, origAgent := outputFormat(context.Background()), agentMode(context.Background())
	t.Cleanup(func() {
		gFlags.cfgFile = originalCfgFile
		gFlags.outputFormat, gFlags.agentMode = origFormat, origAgent
		_ = describeExtensionCmd.Flags().Set("feature-set-metrics", "false")
		_ = describeExtensionCmd.Flags().Set("version", "")
	})
	gFlags.cfgFile = configPath
	gFlags.outputFormat = "json"
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

	if featureSetMetrics {
		_ = describeExtensionCmd.Flags().Set("feature-set-metrics", "true")
	}

	out := captureExtStdout(t, func() {
		if err := describeExtensionCmd.RunE(describeExtensionCmd, []string{name}); err != nil {
			t.Fatalf("describe extension failed: %v", err)
		}
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	return got
}

func captureExtStdout(t *testing.T, fn func()) string {
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

func TestDescribeExtension_FeatureSets_Integration(t *testing.T) {
	srv := httptest.NewServer(describeExtensionMux())
	defer srv.Close()

	t.Run("without --feature-set-metrics: featureSets is a name array", func(t *testing.T) {
		got := runDescribeExtensionJSON(t, srv, "com.example.with-fs", false)

		fs, ok := got["featureSets"].([]any)
		if !ok {
			t.Fatalf("featureSets = %T (%v), want []any", got["featureSets"], got["featureSets"])
		}
		if len(fs) != 2 || fs[0] != "default" || fs[1] != "advanced" {
			t.Errorf("featureSets = %v, want [default advanced]", fs)
		}
	})

	t.Run("with --feature-set-metrics: featureSets is a map with metadata", func(t *testing.T) {
		got := runDescribeExtensionJSON(t, srv, "com.example.with-fs", true)

		fs, ok := got["featureSets"].(map[string]any)
		if !ok {
			t.Fatalf("featureSets = %T (%v), want map", got["featureSets"], got["featureSets"])
		}
		metrics, ok := fs["default"].([]any)
		if !ok || len(metrics) != 2 {
			t.Fatalf("featureSets[default] = %v, want 2 metrics", fs["default"])
		}

		// First metric carries metadata (proves the featureSetsDetails tag is correct).
		m0 := metrics[0].(map[string]any)
		md, ok := m0["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("metrics[0].metadata missing; got %v", m0)
		}
		if md["displayName"] != "Instance uptime" || md["unit"] != "Second" {
			t.Errorf("metadata = %v, want displayName=Instance uptime unit=Second", md)
		}

		// Second metric has no metadata: the field must be omitted, not "metadata": {}.
		m1 := metrics[1].(map[string]any)
		if _, present := m1["metadata"]; present {
			t.Errorf("metrics[1] should omit empty metadata, got %v", m1)
		}
	})

	t.Run("extension with no feature sets omits the field", func(t *testing.T) {
		got := runDescribeExtensionJSON(t, srv, "com.example.no-fs", false)

		if v, present := got["featureSets"]; present {
			t.Errorf("featureSets should be omitted for an extension with none, got %v (%T)", v, v)
		}
	})
}
