package cmd

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

// makeEditorScript creates a temporary script that overwrites its first
// argument with the given content. Uses a .bat file on Windows (invoked via
// "cmd /c"), a .sh file everywhere else. The content is written to a
// separate temp file to avoid shell-escaping issues.
func makeEditorScript(t *testing.T, content string) string {
	t.Helper()

	// Write content to a source file; the script just copies it over.
	src, err := os.CreateTemp("", "editor-src-*")
	if err != nil {
		t.Fatalf("failed to create content file: %v", err)
	}
	if _, err := src.WriteString(content); err != nil {
		t.Fatalf("failed to write content: %v", err)
	}
	_ = src.Close()
	srcPath := src.Name()
	t.Cleanup(func() { os.Remove(srcPath) })

	if runtime.GOOS == "windows" {
		f, err := os.CreateTemp("", "editor-*.bat")
		if err != nil {
			t.Fatalf("failed to create editor script: %v", err)
		}
		_, _ = f.WriteString("@echo off\ncopy /Y \"" + srcPath + "\" \"%~1\"\n")
		_ = f.Close()
		batPath := f.Name()
		t.Cleanup(func() { os.Remove(batPath) })
		// edit_settings.go splits EDITOR on spaces; "cmd /c path" expands to
		// exec.Command("cmd", "/c", batPath, tmpfile).
		return "cmd /c " + batPath
	}

	f, err := os.CreateTemp("", "editor-*.sh")
	if err != nil {
		t.Fatalf("failed to create editor script: %v", err)
	}
	_, _ = f.WriteString("#!/bin/sh\ncp '" + srcPath + "' \"$1\"\n")
	_ = f.Close()
	_ = os.Chmod(f.Name(), 0755)
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestEditSettingsValidateOnly_Success(t *testing.T) {
	const objectID = "test-edit-validate-obj"

	t.Setenv("EDITOR", makeEditorScript(t, `{"enabled":false}`))

	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects/" + objectID: func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"objectId":"` + objectID + `","schemaVersion":"1.0","value":{"enabled":true}}`))
			case http.MethodPut:
				if r.URL.Query().Get("validateOnly") != "true" {
					t.Errorf("expected validateOnly=true, got %q", r.URL.Query().Get("validateOnly"))
				}
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected method %s", r.Method)
			}
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()
	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(editSettingCmd)
	_ = editSettingCmd.Flags().Set("validate-only", "true")
	_ = editSettingCmd.Flags().Set("format", "json")

	if err := editSettingCmd.RunE(editSettingCmd, []string{objectID}); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestEditSettingsValidateOnly_ValidationFailed(t *testing.T) {
	const objectID = "test-edit-validate-fail-obj"

	t.Setenv("EDITOR", makeEditorScript(t, `{"enabled":false}`))

	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects/" + objectID: func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"objectId":"` + objectID + `","schemaVersion":"1.0","value":{"enabled":true}}`))
			case http.MethodPut:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"invalid value"}}`))
			}
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()
	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(editSettingCmd)
	_ = editSettingCmd.Flags().Set("validate-only", "true")
	_ = editSettingCmd.Flags().Set("format", "json")

	err := editSettingCmd.RunE(editSettingCmd, []string{objectID})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("expected error containing 'validation failed', got %q", err.Error())
	}
}
