package apply

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// onProcessStderr runs fn and returns what it wrote to the process's stderr.
func onProcessStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return string(<-done)
}

// The Azure federated-credential instructions are printed for a person to act
// on. An applier given its own stderr must keep every line of them there,
// including the warning for a base URL that does not parse.
func TestAzureFederatedInstructionsUseTheApplierStderr(t *testing.T) {
	const (
		baseURL  = "https://env.example.com"
		objectID = "obj-123"
	)

	t.Run("instructions", func(t *testing.T) {
		var buf bytes.Buffer
		a := (&Applier{}).WithStderr(&buf)
		var warnings []string

		leaked := onProcessStderr(t, func() {
			a.printFederatedInstructions(baseURL, objectID, "https://issuer.example.com", &warnings)
		})

		for _, want := range []string{
			"Further configuration required in Azure Portal",
			"Issuer:    https://issuer.example.com",
			"Subject:   dt:connection-id/obj-123",
			"Audiences: env.example.com/svc-id/com.dynatrace.da",
		} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("applier stderr %q lacks %q", buf.String(), want)
			}
		}
		if leaked != "" {
			t.Errorf("instructions reached the process stderr: %q", leaked)
		}
		if len(warnings) != 1 {
			t.Errorf("warnings = %v, want the portal-setup warning", warnings)
		}
	})

	t.Run("complete instructions", func(t *testing.T) {
		var buf bytes.Buffer
		a := (&Applier{}).WithStderr(&buf)

		leaked := onProcessStderr(t, func() {
			a.printFederatedCompleteInstructions(baseURL, objectID, "my-connection", "https://issuer.example.com")
		})

		for _, want := range []string{"2. Create Federated Credential:", "dt:connection-id/obj-123", "env.example.com/svc-id/com.dynatrace.da"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("applier stderr %q lacks %q", buf.String(), want)
			}
		}
		if leaked != "" {
			t.Errorf("instructions reached the process stderr: %q", leaked)
		}
	})

	t.Run("error snippet", func(t *testing.T) {
		var buf bytes.Buffer
		a := (&Applier{}).WithStderr(&buf)

		leaked := onProcessStderr(t, func() {
			a.printFederatedErrorSnippet(baseURL, objectID, "client-1", "https://issuer.example.com")
		})

		if !strings.Contains(buf.String(), "az ad app federated-credential create --id \"client-1\"") {
			t.Errorf("applier stderr %q lacks the az command", buf.String())
		}
		if leaked != "" {
			t.Errorf("snippet reached the process stderr: %q", leaked)
		}
	})

	t.Run("an unparseable base URL is reported on the applier stderr", func(t *testing.T) {
		const badURL = "http://[::1"
		var buf bytes.Buffer
		a := (&Applier{}).WithStderr(&buf)

		leaked := onProcessStderr(t, func() {
			a.printFederatedInstructions(badURL, objectID, "", nil)
			a.printFederatedCompleteInstructions(badURL, objectID, "my-connection", "")
		})

		if got := strings.Count(buf.String(), "Could not parse base URL for instructions"); got != 2 {
			t.Errorf("applier stderr %q has %d parse warnings, want 2", buf.String(), got)
		}
		if leaked != "" {
			t.Errorf("a parse warning reached the process stderr: %q", leaked)
		}
	})
}
