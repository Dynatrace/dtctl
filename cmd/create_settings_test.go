package cmd

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

func TestCreateSettingsValidateOnly_Success(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if r.URL.Query().Get("validateOnly") != "true" {
				t.Errorf("expected validateOnly=true, got %q", r.URL.Query().Get("validateOnly"))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	settingsFile := testutil.CreateTempFile(t, `{"key": "value"}`, "settings-*.json")

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()
	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(createSettingsCmd)
	_ = createSettingsCmd.Flags().Set("file", settingsFile)
	_ = createSettingsCmd.Flags().Set("schema", "builtin:alerting.profile")
	_ = createSettingsCmd.Flags().Set("scope", "environment")
	_ = createSettingsCmd.Flags().Set("validate-only", "true")

	if err := createSettingsCmd.RunE(createSettingsCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestCreateSettingsValidateOnly_ValidationFailed(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "invalid value"}}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	settingsFile := testutil.CreateTempFile(t, `{"key": "value"}`, "settings-*.json")

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()
	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(createSettingsCmd)
	_ = createSettingsCmd.Flags().Set("file", settingsFile)
	_ = createSettingsCmd.Flags().Set("schema", "builtin:alerting.profile")
	_ = createSettingsCmd.Flags().Set("scope", "environment")
	_ = createSettingsCmd.Flags().Set("validate-only", "true")

	err := createSettingsCmd.RunE(createSettingsCmd, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("expected error containing 'validation failed', got %q", err.Error())
	}
}
