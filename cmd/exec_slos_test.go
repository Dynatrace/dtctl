package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/slo"
)

// floatPtr returns a pointer to a float64
func floatPtr(f float64) *float64 {
	return &f
}

// TestExecSLO_ImmediateResults_TableOutput tests that immediate evaluation results
// display correctly in table format with actual numeric values, not pointer addresses.
// This test verifies the fix: using PrintList() instead of Print() for slices.
func TestExecSLO_ImmediateResults_TableOutput(t *testing.T) {
	tests := []struct {
		name           string
		responseBody   slo.EvaluationResponse
		expectError    bool
		validateOutput func(*testing.T, string)
	}{
		{
			name: "single evaluation result with float values",
			responseBody: slo.EvaluationResponse{
				Definition: &slo.SLO{
					ID:   "slo-123",
					Name: "Test SLO",
				},
				EvaluationResults: []slo.EvaluationResult{
					{
						Criteria:    "now-7d -> now",
						Status:      "FAILURE",
						Value:       floatPtr(95.39163051360929),
						ErrorBudget: floatPtr(-1.608369486390714),
					},
				},
			},
			expectError: false,
			validateOutput: func(t *testing.T, output string) {
				// CRITICAL: Verify no pointer addresses appear (the bug we fixed)
				if strings.Contains(output, "0x") {
					t.Errorf("Output contains pointer address instead of value: %s", output)
				}

				// Verify table headers are present
				if !strings.Contains(output, "CRITERIA") {
					t.Error("Expected CRITERIA header not found in table output")
				}
				if !strings.Contains(output, "STATUS") {
					t.Error("Expected STATUS header not found in table output")
				}
				if !strings.Contains(output, "VALUE") {
					t.Error("Expected VALUE header not found in table output")
				}
				if !strings.Contains(output, "ERROR BUDGET") {
					t.Error("Expected ERROR BUDGET header not found in table output")
				}

				// Verify actual numeric values are displayed
				if !strings.Contains(output, "95.39") {
					t.Errorf("Expected value '95.39' not found in output: %s", output)
				}
				if !strings.Contains(output, "-1.60") && !strings.Contains(output, "-1.61") {
					t.Errorf("Expected error budget '-1.60' or '-1.61' not found in output: %s", output)
				}

				// Verify status and criteria
				if !strings.Contains(output, "FAILURE") {
					t.Error("Expected status 'FAILURE' not found in output")
				}
				if !strings.Contains(output, "now-7d -> now") {
					t.Error("Expected criteria 'now-7d -> now' not found in output")
				}
			},
		},
		{
			name: "multiple evaluation results",
			responseBody: slo.EvaluationResponse{
				Definition: &slo.SLO{
					ID:   "slo-multi",
					Name: "Multi-criteria SLO",
				},
				EvaluationResults: []slo.EvaluationResult{
					{
						Criteria:    "now-7d -> now",
						Status:      "SUCCESS",
						Value:       floatPtr(99.95),
						ErrorBudget: floatPtr(0.95),
					},
					{
						Criteria:    "now-30d -> now",
						Status:      "WARNING",
						Value:       floatPtr(98.50),
						ErrorBudget: floatPtr(-0.50),
					},
				},
			},
			expectError: false,
			validateOutput: func(t *testing.T, output string) {
				// Verify no pointer addresses
				if strings.Contains(output, "0x") {
					t.Errorf("Output contains pointer address: %s", output)
				}

				// Verify both results are displayed
				if !strings.Contains(output, "99.95") {
					t.Error("First result value not found")
				}
				// Try both 98.50 and 98.5 (trailing zero might be removed)
				if !strings.Contains(output, "98.5") {
					t.Errorf("Second result value not found in output:\n%s", output)
				}
				if !strings.Contains(output, "SUCCESS") {
					t.Error("First result status not found")
				}
				if !strings.Contains(output, "WARNING") {
					t.Error("Second result status not found")
				}
			},
		},
		{
			name: "evaluation result with nil pointer values",
			responseBody: slo.EvaluationResponse{
				Definition: &slo.SLO{
					ID:   "slo-nil",
					Name: "SLO with nil values",
				},
				EvaluationResults: []slo.EvaluationResult{
					{
						Criteria:    "now-1h -> now",
						Status:      "PENDING",
						Value:       nil, // Nil pointer - should display empty/dash
						ErrorBudget: nil,
					},
				},
			},
			expectError: false,
			validateOutput: func(t *testing.T, output string) {
				// Verify no pointer addresses (nil should not print as 0x0 or similar)
				if strings.Contains(output, "0x") {
					t.Errorf("Output contains pointer address for nil value: %s", output)
				}

				// Verify headers and criteria are present
				if !strings.Contains(output, "CRITERIA") {
					t.Error("Expected CRITERIA header not found")
				}
				if !strings.Contains(output, "now-1h -> now") {
					t.Error("Expected criteria not found")
				}
				if !strings.Contains(output, "PENDING") {
					t.Error("Expected status not found")
				}
			},
		},
		{
			name: "evaluation with success status and positive error budget",
			responseBody: slo.EvaluationResponse{
				Definition: &slo.SLO{
					ID:   "slo-success",
					Name: "Successful SLO",
				},
				EvaluationResults: []slo.EvaluationResult{
					{
						Criteria:    "now-14d -> now",
						Status:      "SUCCESS",
						Value:       floatPtr(99.99),
						ErrorBudget: floatPtr(2.99),
					},
				},
			},
			expectError: false,
			validateOutput: func(t *testing.T, output string) {
				// Verify no pointer addresses
				if strings.Contains(output, "0x") {
					t.Errorf("Output contains pointer address: %s", output)
				}

				// Verify positive error budget displays correctly
				if !strings.Contains(output, "2.99") {
					t.Errorf("Expected positive error budget not found: %s", output)
				}
				if !strings.Contains(output, "SUCCESS") {
					t.Error("Expected SUCCESS status not found")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/slo/v1/slos/evaluation:start" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			// Create client pointing to mock server
			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			// Create SLO handler
			handler := slo.NewHandler(c)

			// Call Evaluate to get the response
			sloID := "test-slo-id"
			evalResult, err := handler.Evaluate(sloID)

			if (err != nil) != tt.expectError {
				t.Errorf("Evaluate() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				return
			}

			// Now test the output formatting (the fix we're testing)
			// Simulate what exec_slos.go does at line 70
			var buf bytes.Buffer
			printer := output.NewPrinterWithWriter("table", &buf)

			// CRITICAL TEST: Use PrintList (the fix) instead of Print
			err = printer.PrintList(evalResult.EvaluationResults)
			if err != nil {
				t.Fatalf("PrintList() failed: %v", err)
			}

			outputStr := buf.String()

			// Validate output
			if tt.validateOutput != nil {
				tt.validateOutput(t, outputStr)
			}
		})
	}
}

// TestExecSLO_OutputFormats tests that evaluation results work correctly
// with different output formats (JSON, YAML, table)
func TestExecSLO_OutputFormats(t *testing.T) {
	testResponse := slo.EvaluationResponse{
		Definition: &slo.SLO{
			ID:   "slo-format-test",
			Name: "Format Test SLO",
		},
		EvaluationResults: []slo.EvaluationResult{
			{
				Criteria:    "now-7d -> now",
				Status:      "FAILURE",
				Value:       floatPtr(95.39163051360929),
				ErrorBudget: floatPtr(-1.608369486390714),
			},
		},
	}

	tests := []struct {
		name           string
		format         string
		validateOutput func(*testing.T, string)
	}{
		{
			name:   "json output format",
			format: "json",
			validateOutput: func(t *testing.T, output string) {
				// Verify valid JSON
				var result interface{}
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Errorf("Output is not valid JSON: %v", err)
				}

				// Verify numeric values are present as JSON numbers (not strings)
				if !strings.Contains(output, "95.39163051360929") {
					t.Error("Expected numeric value not found in JSON output")
				}
				if !strings.Contains(output, "-1.608369486390714") {
					t.Error("Expected error budget not found in JSON output")
				}

				// Verify structure
				if !strings.Contains(output, "\"criteria\"") {
					t.Error("Expected 'criteria' field not found in JSON")
				}
				if !strings.Contains(output, "\"value\"") {
					t.Error("Expected 'value' field not found in JSON")
				}
			},
		},
		{
			name:   "yaml output format",
			format: "yaml",
			validateOutput: func(t *testing.T, output string) {
				// Verify YAML structure (basic check)
				if !strings.Contains(output, "criteria:") {
					t.Error("Expected 'criteria:' field not found in YAML output")
				}
				if !strings.Contains(output, "value:") {
					t.Error("Expected 'value:' field not found in YAML output")
				}

				// Verify numeric values
				if !strings.Contains(output, "95.39") {
					t.Error("Expected value not found in YAML output")
				}
			},
		},
		{
			name:   "table output format (default)",
			format: "table",
			validateOutput: func(t *testing.T, output string) {
				// Verify table headers
				if !strings.Contains(output, "CRITERIA") {
					t.Error("Expected CRITERIA header not found")
				}
				if !strings.Contains(output, "VALUE") {
					t.Error("Expected VALUE header not found")
				}

				// CRITICAL: Verify no pointer addresses
				if strings.Contains(output, "0x") {
					t.Errorf("Table output contains pointer address: %s", output)
				}

				// Verify numeric values
				if !strings.Contains(output, "95.39") {
					t.Error("Expected value not found in table output")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(testResponse)
			}))
			defer server.Close()

			// Create client
			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			// Get evaluation results
			handler := slo.NewHandler(c)
			evalResult, err := handler.Evaluate("test-slo")
			if err != nil {
				t.Fatalf("Evaluate() failed: %v", err)
			}

			// Test output formatting
			var buf bytes.Buffer
			var printer output.Printer

			if tt.format == "json" || tt.format == "yaml" {
				printer = output.NewPrinterWithWriter(tt.format, &buf)
				// For JSON/YAML, print the full response
				err = printer.Print(evalResult)
			} else {
				// For table format, use PrintList on the results array
				printer = output.NewPrinterWithWriter(tt.format, &buf)
				err = printer.PrintList(evalResult.EvaluationResults)
			}

			if err != nil {
				t.Fatalf("Print failed: %v", err)
			}

			outputStr := buf.String()

			// Validate output
			if tt.validateOutput != nil {
				tt.validateOutput(t, outputStr)
			}
		})
	}
}

// TestExecSLO_ErrorCases tests error handling for edge cases
func TestExecSLO_ErrorCases(t *testing.T) {
	tests := []struct {
		name          string
		responseBody  interface{}
		statusCode    int
		expectError   bool
		errorContains string
	}{
		{
			name: "no evaluation token and no immediate results",
			responseBody: slo.EvaluationResponse{
				// Empty response - no token, no results
				EvaluationToken:   "",
				EvaluationResults: nil,
			},
			statusCode:    200,
			expectError:   false, // Handler doesn't error, but exec command should
			errorContains: "",
		},
		{
			name:          "API returns 404 - SLO not found",
			responseBody:  map[string]string{"error": "Not found"},
			statusCode:    404,
			expectError:   true,
			errorContains: "Not found",
		},
		{
			name: "empty evaluation results array",
			responseBody: slo.EvaluationResponse{
				Definition: &slo.SLO{
					ID:   "slo-empty",
					Name: "Empty Results SLO",
				},
				EvaluationResults: []slo.EvaluationResult{}, // Empty array
			},
			statusCode:  200,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := slo.NewHandler(c)
			_, err = handler.Evaluate("test-slo")

			if (err != nil) != tt.expectError {
				t.Errorf("Evaluate() error = %v, expectError %v", err, tt.expectError)
			}

			if tt.expectError && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			}
		})
	}
}

// TestPrintList_vs_Print tests the difference between Print and PrintList
// to demonstrate why the fix was necessary
func TestPrintList_vs_Print(t *testing.T) {
	testResults := []slo.EvaluationResult{
		{
			Criteria:    "test-criteria",
			Status:      "SUCCESS",
			Value:       floatPtr(99.5),
			ErrorBudget: floatPtr(1.5),
		},
	}

	t.Run("Print on slice shows pointer addresses (bug)", func(t *testing.T) {
		var buf bytes.Buffer
		printer := output.NewPrinterWithWriter("table", &buf)

		// This is what the code did BEFORE the fix
		err := printer.Print(testResults)
		if err != nil {
			// Print() on a slice might fail or produce bad output
			// We don't error here because it's the "old behavior"
			t.Logf("Print() on slice produced error (expected): %v", err)
		}

		output := buf.String()
		t.Logf("Print() output: %s", output)

		// The old behavior would show the slice representation with pointer addresses
		// We're documenting this, not asserting it fails, since Print() might just
		// fall back to fmt.Fprintln which shows the raw Go representation
	})

	t.Run("PrintList on slice shows table correctly (fix)", func(t *testing.T) {
		var buf bytes.Buffer
		printer := output.NewPrinterWithWriter("table", &buf)

		// This is what the code does AFTER the fix
		err := printer.PrintList(testResults)
		if err != nil {
			t.Fatalf("PrintList() failed: %v", err)
		}

		output := buf.String()

		// Verify proper table output
		if strings.Contains(output, "0x") {
			t.Error("PrintList() should not show pointer addresses")
		}
		if !strings.Contains(output, "99.5") {
			t.Error("PrintList() should show actual values")
		}
		if !strings.Contains(output, "CRITERIA") {
			t.Error("PrintList() should show table headers")
		}
	})
}
