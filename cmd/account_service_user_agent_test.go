package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

type serviceUserAgentEnvelope struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Context struct {
		Verb     string `json:"verb"`
		Resource string `json:"resource"`
	} `json:"context"`
}

func decodeServiceUserAgentEnvelope(t *testing.T, output, verb string) serviceUserAgentEnvelope {
	t.Helper()
	var envelope serviceUserAgentEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode agent envelope: %v\noutput=%q", err, output)
	}
	if !envelope.OK {
		t.Fatal("agent envelope ok = false, want true")
	}
	if envelope.Context.Verb != verb || envelope.Context.Resource != "service-user" {
		t.Fatalf("context = verb %q resource %q, want %q and service-user", envelope.Context.Verb, envelope.Context.Resource, verb)
	}
	return envelope
}

func TestServiceUserAgentOutputEnvelopes(t *testing.T) {
	oldAgentMode, oldPlainMode, oldOutputFormat, oldDryRun := agentMode(context.Background()), plainMode(context.Background()), outputFormat(context.Background()), dryRun(context.Background())
	oldSetup, oldSafetySetup := setupServiceUserAccount, setupServiceUserAccountWithSafety
	nameFlag := accountCreateServiceUserCmd.Flags().Lookup("name")
	descriptionFlag := accountCreateServiceUserCmd.Flags().Lookup("description")
	oldName, oldDescription := nameFlag.Value.String(), descriptionFlag.Value.String()
	oldNameChanged, oldDescriptionChanged := nameFlag.Changed, descriptionFlag.Changed
	outputFlag := rootCmd.PersistentFlags().Lookup("output")
	oldOutputChanged := outputFlag.Changed
	t.Cleanup(func() {
		gFlags.agentMode, gFlags.plainMode, gFlags.outputFormat, gFlags.dryRun = oldAgentMode, oldPlainMode, oldOutputFormat, oldDryRun
		setupServiceUserAccount, setupServiceUserAccountWithSafety = oldSetup, oldSafetySetup
		// Reset name via Reset() because nonEmptyStringValue rejects Set("").
		if oldName == "" {
			if v, ok := nameFlag.Value.(*nonEmptyStringValue); ok {
				v.Reset()
			}
		} else {
			_ = accountCreateServiceUserCmd.Flags().Set("name", oldName)
		}
		_ = accountCreateServiceUserCmd.Flags().Set("description", oldDescription)
		nameFlag.Changed, descriptionFlag.Changed = oldNameChanged, oldDescriptionChanged
		outputFlag.Changed = oldOutputChanged
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"results":[{"uid":"00000000-0000-4000-8000-000000000101","email":"automation-alpha@example.invalid","name":"automation-alpha","createdAt":"2026-03-01T12:00:00Z"}]}`))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"uid":"00000000-0000-4000-8000-000000000101","email":"automation-alpha@example.invalid","name":"automation-alpha","createdAt":"2026-03-01T12:00:00Z"}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	client, err := httpclient.New(server.URL, httpclient.WithToken("dt0c01.synthetic"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}

	gFlags.agentMode, gFlags.plainMode, gFlags.outputFormat = true, true, ""
	outputFlag.Changed = false
	setupServiceUserAccount = func() (*httpclient.Client, string, error) {
		return client, "00000000-0000-4000-8000-000000000001", nil
	}
	setupServiceUserAccountWithSafety = func(safety.Operation) (*httpclient.Client, string, error) {
		return client, "00000000-0000-4000-8000-000000000001", nil
	}
	_ = accountCreateServiceUserCmd.Flags().Set("name", "automation-alpha")
	_ = accountCreateServiceUserCmd.Flags().Set("description", "Synthetic integration identity")

	run := func(t *testing.T, verb string, execute func() error) serviceUserAgentEnvelope {
		t.Helper()
		var runErr error
		output := captureStdout(t, func() { runErr = execute() })
		if runErr != nil {
			t.Fatalf("command error: %v", runErr)
		}
		return decodeServiceUserAgentEnvelope(t, output, verb)
	}

	t.Run("list success", func(t *testing.T) {
		gFlags.dryRun = false
		envelope := run(t, "list", func() error { return accountListServiceUserCmd.RunE(accountListServiceUserCmd, nil) })
		var users []map[string]any
		if err := json.Unmarshal(envelope.Result, &users); err != nil || len(users) != 1 || users[0]["uid"] == "" {
			t.Fatalf("list result = %s, error = %v", envelope.Result, err)
		}
	})

	t.Run("create success", func(t *testing.T) {
		gFlags.dryRun = false
		envelope := run(t, "create", func() error { return accountCreateServiceUserCmd.RunE(accountCreateServiceUserCmd, nil) })
		var result map[string]any
		if err := json.Unmarshal(envelope.Result, &result); err != nil || result["uid"] == "" {
			t.Fatalf("create result = %s, error = %v", envelope.Result, err)
		}
	})

	t.Run("delete success", func(t *testing.T) {
		gFlags.dryRun = false
		envelope := run(t, "delete", func() error {
			return accountDeleteServiceUserCmd.RunE(accountDeleteServiceUserCmd, []string{"00000000-0000-4000-8000-000000000101"})
		})
		var result map[string]any
		if err := json.Unmarshal(envelope.Result, &result); err != nil || result["deleted"] != true {
			t.Fatalf("delete result = %s, error = %v", envelope.Result, err)
		}
	})

	t.Run("create dry run", func(t *testing.T) {
		gFlags.dryRun = true
		envelope := run(t, "create", func() error { return accountCreateServiceUserCmd.RunE(accountCreateServiceUserCmd, nil) })
		var result map[string]any
		if err := json.Unmarshal(envelope.Result, &result); err != nil || result["dryRun"] != true {
			t.Fatalf("create dry-run result = %s, error = %v", envelope.Result, err)
		}
	})

	t.Run("delete dry run", func(t *testing.T) {
		gFlags.dryRun = true
		envelope := run(t, "delete", func() error {
			return accountDeleteServiceUserCmd.RunE(accountDeleteServiceUserCmd, []string{"00000000-0000-4000-8000-000000000101"})
		})
		var result map[string]any
		if err := json.Unmarshal(envelope.Result, &result); err != nil || result["dryRun"] != true {
			t.Fatalf("delete dry-run result = %s, error = %v", envelope.Result, err)
		}
	})
}
