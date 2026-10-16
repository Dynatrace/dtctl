package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}

	os.Stdout = w
	run()
	_ = w.Close()
	os.Stdout = origStdout

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read stdout capture: %v", err)
	}

	return string(data)
}

func TestPrintBreakpointMessage_Table(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.outputFormat = "table"
	gFlags.agentMode = false
	gFlags.plainMode = true

	output := captureStdout(t, func() {
		if err := printBreakpointMessage(context.Background(), "create", "Created breakpoint at A.java:10"); err != nil {
			t.Fatalf("printBreakpointMessage returned error: %v", err)
		}
	})

	if !strings.Contains(output, "Created breakpoint at A.java:10") {
		t.Fatalf("expected output message, got: %q", output)
	}
}

func TestPrintBreakpointMessage_Agent(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.outputFormat = ""
	gFlags.agentMode = true
	gFlags.plainMode = true

	output := captureStdout(t, func() {
		if err := printBreakpointMessage(context.Background(), "delete", "Deletion cancelled"); err != nil {
			t.Fatalf("printBreakpointMessage returned error: %v", err)
		}
	})

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Message string `json:"message"`
		} `json:"result"`
		Context struct {
			Verb     string `json:"verb"`
			Resource string `json:"resource"`
		} `json:"context"`
	}

	if err := json.NewDecoder(bytes.NewBufferString(output)).Decode(&envelope); err != nil {
		t.Fatalf("failed to decode agent output: %v\noutput=%q", err, output)
	}

	if !envelope.OK {
		t.Fatalf("expected ok=true envelope, got false")
	}
	if envelope.Result.Message != "Deletion cancelled" {
		t.Fatalf("unexpected message: %q", envelope.Result.Message)
	}
	if envelope.Context.Verb != "delete" || envelope.Context.Resource != "breakpoint" {
		t.Fatalf("unexpected context: verb=%q resource=%q", envelope.Context.Verb, envelope.Context.Resource)
	}
}
