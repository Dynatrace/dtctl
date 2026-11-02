package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/resources/analyzer"
)

// TestVerifyAnalyzer_RejectsUnsupportedOutputFormat exercises the up-front
// output-format guard: an unsupported format (e.g. csv) must be rejected with a
// clear error before any input parsing or network call, matching "verify query"
// rather than silently falling back to the human verdict.
func TestVerifyAnalyzer_RejectsUnsupportedOutputFormat(t *testing.T) {
	for _, format := range []string{"csv", "wide", "chart", "xml"} {
		t.Run(format, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("output", "", "")
			addAnalyzerInputFlags(cmd)
			if err := cmd.Flags().Set("output", format); err != nil {
				t.Fatalf("set output flag: %v", err)
			}
			// A valid input source is present, so the only thing that can fail
			// is the format guard (which runs before the network call).
			if err := cmd.Flags().Set("query", "timeseries avg(dt.host.cpu.usage)"); err != nil {
				t.Fatalf("set query flag: %v", err)
			}

			err := verifyAnalyzerCmd.RunE(cmd, []string{"dt.statistics.GenericForecastAnalyzer"})
			if err == nil {
				t.Fatalf("expected error for unsupported output format %q, got nil", format)
			}
			if !strings.Contains(err.Error(), "unsupported output format") {
				t.Errorf("expected unsupported-format error for %q, got %v", format, err)
			}
		})
	}
}

func TestGetAnalyzerValidateExitCode_Valid(t *testing.T) {
	result := &analyzer.ValidationResult{Valid: true}
	if got := getAnalyzerValidateExitCode(result, nil); got != 0 {
		t.Errorf("expected exit code 0, got %d", got)
	}
}

func TestGetAnalyzerValidateExitCode_Invalid(t *testing.T) {
	result := &analyzer.ValidationResult{Valid: false}
	if got := getAnalyzerValidateExitCode(result, nil); got != 1 {
		t.Errorf("expected exit code 1, got %d", got)
	}
}

func TestGetAnalyzerValidateExitCode_NilResult(t *testing.T) {
	if got := getAnalyzerValidateExitCode(nil, nil); got != 1 {
		t.Errorf("expected exit code 1 for nil result, got %d", got)
	}
}

func TestGetAnalyzerValidateExitCode_AuthError(t *testing.T) {
	if got := getAnalyzerValidateExitCode(nil, errors.New("request failed with status 401")); got != 2 {
		t.Errorf("expected exit code 2 for 401, got %d", got)
	}
	if got := getAnalyzerValidateExitCode(nil, errors.New("request failed with status 403")); got != 2 {
		t.Errorf("expected exit code 2 for 403, got %d", got)
	}
}

func TestGetAnalyzerValidateExitCode_NetworkError(t *testing.T) {
	if got := getAnalyzerValidateExitCode(nil, errors.New("request failed with status 500")); got != 3 {
		t.Errorf("expected exit code 3 for 500, got %d", got)
	}
	if got := getAnalyzerValidateExitCode(nil, errors.New("dial tcp: connection refused")); got != 3 {
		t.Errorf("expected exit code 3 for connection error, got %d", got)
	}
	if got := getAnalyzerValidateExitCode(nil, errors.New("context deadline exceeded (timeout)")); got != 3 {
		t.Errorf("expected exit code 3 for timeout, got %d", got)
	}
}

func TestGetAnalyzerValidateExitCode_OtherError(t *testing.T) {
	if got := getAnalyzerValidateExitCode(nil, errors.New("some client-side problem")); got != 1 {
		t.Errorf("expected exit code 1 for generic error, got %d", got)
	}
}
