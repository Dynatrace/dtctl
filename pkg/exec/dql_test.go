package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/aidetect"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"

	"gopkg.in/yaml.v3"
)

// mappingsByName collapses the flattened column-type mappings into a name→type
// map for order-independent assertions.
func mappingsByName(m []output.ColumnTypeMapping) map[string]string {
	out := make(map[string]string, len(m))
	for _, c := range m {
		out[c.Name] = c.Type
	}
	return out
}

func TestColumnTypeMappings(t *testing.T) {
	t.Run("nil when no type info", func(t *testing.T) {
		r := &DQLQueryResponse{Result: &DQLResult{Records: []map[string]interface{}{{"a": 1}}}}
		if got := columnTypeMappings(r); got != nil {
			t.Errorf("expected nil, got %#v", got)
		}
		if got := columnTypeMappings(&DQLQueryResponse{}); got != nil {
			t.Errorf("expected nil for empty response, got %#v", got)
		}
	})

	t.Run("flattens a single group", func(t *testing.T) {
		r := &DQLQueryResponse{Result: &DQLResult{
			Types: []sdkquery.ColumnTypes{
				{IndexRange: []int{0, 1}, Mappings: map[string]sdkquery.ColumnType{
					"host":  {Type: "string"},
					"count": {Type: "long"},
				}},
			},
		}}
		got := mappingsByName(columnTypeMappings(r))
		if len(got) != 2 || got["host"] != "string" || got["count"] != "long" {
			t.Errorf("got %#v, want host=string count=long", got)
		}
	})

	t.Run("first group wins on disagreement across groups", func(t *testing.T) {
		r := &DQLQueryResponse{Result: &DQLResult{
			Types: []sdkquery.ColumnTypes{
				{IndexRange: []int{0, 0}, Mappings: map[string]sdkquery.ColumnType{"v": {Type: "long"}}},
				{IndexRange: []int{1, 1}, Mappings: map[string]sdkquery.ColumnType{"v": {Type: "string"}}},
			},
		}}
		got := mappingsByName(columnTypeMappings(r))
		if len(got) != 1 || got["v"] != "long" {
			t.Errorf("got %#v, want v=long (first group wins)", got)
		}
	})

	t.Run("merges distinct columns across groups", func(t *testing.T) {
		r := &DQLQueryResponse{Result: &DQLResult{
			Types: []sdkquery.ColumnTypes{
				{IndexRange: []int{0, 0}, Mappings: map[string]sdkquery.ColumnType{"a": {Type: "long"}}},
				{IndexRange: []int{1, 1}, Mappings: map[string]sdkquery.ColumnType{"b": {Type: "double"}}},
			},
		}}
		got := mappingsByName(columnTypeMappings(r))
		if len(got) != 2 || got["a"] != "long" || got["b"] != "double" {
			t.Errorf("got %#v, want a=long b=double", got)
		}
	})
}

func TestDQLExecutor_ExecuteQueryWithOptions_CustomHeaders(t *testing.T) {
	tests := []struct {
		name                    string
		maxResultRecords        int64
		maxReturnedRecords      int64
		expectMaxResultHeader   bool
		expectMaxReturnedHeader bool
	}{
		{
			name:                    "no custom headers",
			maxResultRecords:        0,
			maxReturnedRecords:      0,
			expectMaxResultHeader:   false,
			expectMaxReturnedHeader: false,
		},
		{
			name:                    "max-result-records only",
			maxResultRecords:        5000,
			maxReturnedRecords:      0,
			expectMaxResultHeader:   true,
			expectMaxReturnedHeader: false,
		},
		{
			name:                    "max-returned-records only",
			maxResultRecords:        0,
			maxReturnedRecords:      10000,
			expectMaxResultHeader:   false,
			expectMaxReturnedHeader: true,
		},
		{
			name:                    "both custom headers",
			maxResultRecords:        5000,
			maxReturnedRecords:      10000,
			expectMaxResultHeader:   true,
			expectMaxReturnedHeader: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			var receivedRequest DQLQueryRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Decode the request body to check parameters
				_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

				// Return a successful query response
				response := DQLQueryResponse{
					State: "SUCCEEDED",
					Result: &DQLResult{
						Records: []map[string]interface{}{
							{"test": "value"},
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()

			// Create client pointing to test server
			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			executor := NewDQLExecutor(c)

			// Execute query with options
			opts := DQLExecuteOptions{
				MaxResultRecords: tt.maxResultRecords,
				MaxResultBytes:   tt.maxReturnedRecords,
			}

			_, err = executor.ExecuteQueryWithOptions("fetch logs", opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify request body parameters
			if tt.expectMaxResultHeader {
				if receivedRequest.MaxResultRecords != 5000 {
					t.Errorf("expected MaxResultRecords to be 5000, got %d", receivedRequest.MaxResultRecords)
				}
			} else {
				if receivedRequest.MaxResultRecords != 0 {
					t.Errorf("expected MaxResultRecords to be 0, got %d", receivedRequest.MaxResultRecords)
				}
			}

			if tt.expectMaxReturnedHeader {
				if receivedRequest.MaxResultBytes != 10000 {
					t.Errorf("expected MaxResultBytes to be 10000, got %d", receivedRequest.MaxResultBytes)
				}
			} else {
				if receivedRequest.MaxResultBytes != 0 {
					t.Errorf("expected MaxResultBytes to be 0, got %d", receivedRequest.MaxResultBytes)
				}
			}
		})
	}
}

func TestDQLExecutor_ExecuteQuery_BackwardCompatibility(t *testing.T) {
	// Test that the ExecuteQuery method still works for backward compatibility
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{
					{"test": "value"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	result, err := executor.ExecuteQuery("fetch logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.State != "SUCCEEDED" {
		t.Errorf("expected state SUCCEEDED, got %s", result.State)
	}

	if len(result.Result.Records) != 1 {
		t.Errorf("expected 1 record, got %d", len(result.Result.Records))
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_PollingWithBodyParams(t *testing.T) {
	// Test that body parameters are sent in the initial request and polling works
	callCount := 0
	var receivedRequest DQLQueryRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			// Capture the initial request body
			_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

			// First call - return RUNNING state
			response := DQLQueryResponse{
				State:        "RUNNING",
				RequestToken: "test-token-123",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/platform/storage/query/v1/query:poll" {
			// Poll call - just return success
			response := DQLQueryResponse{
				State: "SUCCEEDED",
				Result: &DQLResult{
					Records: []map[string]interface{}{
						{"test": "value"},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	opts := DQLExecuteOptions{
		MaxResultRecords: 5000,
		MaxResultBytes:   10000,
	}

	_, err = executor.ExecuteQueryWithOptions("fetch logs", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify we made both calls (execute and poll)
	if callCount != 2 {
		t.Errorf("expected 2 API calls (execute + poll), got %d", callCount)
	}

	// Verify body parameters were sent in the initial request
	if receivedRequest.MaxResultRecords != 5000 {
		t.Errorf("expected MaxResultRecords to be 5000 in initial request, got %d",
			receivedRequest.MaxResultRecords)
	}

	if receivedRequest.MaxResultBytes != 10000 {
		t.Errorf("expected MaxResultBytes to be 10000 in initial request, got %d",
			receivedRequest.MaxResultBytes)
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_ErrorHandling(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		responseBody   string
		expectedErrMsg string
	}{
		{
			name:           "bad request",
			statusCode:     http.StatusBadRequest,
			responseBody:   "Invalid query",
			expectedErrMsg: "query failed with status 400",
		},
		{
			name:           "unauthorized",
			statusCode:     http.StatusUnauthorized,
			responseBody:   "Unauthorized",
			expectedErrMsg: "query failed with status 401",
		},
		{
			name:           "internal server error",
			statusCode:     http.StatusInternalServerError,
			responseBody:   "Internal error",
			expectedErrMsg: "query failed with status 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			executor := NewDQLExecutor(c)

			_, err = executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if err.Error()[:len(tt.expectedErrMsg)] != tt.expectedErrMsg {
				t.Errorf("expected error to start with '%s', got '%s'", tt.expectedErrMsg, err.Error())
			}
		})
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_SingleQuoteHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"single quotes are not allowed","details":{"errorType":"PARSE_ERROR_SINGLE_QUOTES"}}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = NewDQLExecutor(c).ExecuteQueryWithOptions(`fetch logs | filter status == 'ERROR'`, DQLExecuteOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// The enriched message must guide the user toward double quotes.
	msg := err.Error()
	if !strings.Contains(msg, `double quotes`) {
		t.Errorf("expected hint about double quotes, got:\n%s", msg)
	}
	if !strings.Contains(msg, `status == "ERROR"`) {
		t.Errorf("expected a corrected example in the hint, got:\n%s", msg)
	}

	// The underlying *QueryError must remain in the chain so callers (agent
	// envelope, wait fast-fail) can still classify it.
	var qErr *QueryError
	if !errors.As(err, &qErr) {
		t.Fatalf("expected *QueryError to remain in the error chain, got %T", err)
	}
	if qErr.ErrorType != "PARSE_ERROR_SINGLE_QUOTES" {
		t.Errorf("ErrorType = %q, want PARSE_ERROR_SINGLE_QUOTES", qErr.ErrorType)
	}
	if qErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", qErr.StatusCode)
	}
}

func TestDQLQueryResponse_GetNotifications(t *testing.T) {
	tests := []struct {
		name             string
		response         DQLQueryResponse
		expectedCount    int
		expectedSeverity string
		expectedMessage  string
	}{
		{
			name: "notifications in top-level metadata",
			response: DQLQueryResponse{
				State: "SUCCEEDED",
				Metadata: &DQLMetadata{
					Grail: &GrailMetadata{
						Notifications: []QueryNotification{
							{
								Severity: "WARNING",
								Message:  "Scan limit reached",
							},
						},
					},
				},
			},
			expectedCount:    1,
			expectedSeverity: "WARNING",
			expectedMessage:  "Scan limit reached",
		},
		{
			name: "notifications in result metadata",
			response: DQLQueryResponse{
				State: "SUCCEEDED",
				Result: &DQLResult{
					Records: []map[string]interface{}{{"test": "value"}},
					Metadata: &DQLMetadata{
						Grail: &GrailMetadata{
							Notifications: []QueryNotification{
								{
									Severity: "WARNING",
									Message:  "Result truncated",
								},
							},
						},
					},
				},
			},
			expectedCount:    1,
			expectedSeverity: "WARNING",
			expectedMessage:  "Result truncated",
		},
		{
			name: "no notifications",
			response: DQLQueryResponse{
				State: "SUCCEEDED",
				Result: &DQLResult{
					Records: []map[string]interface{}{{"test": "value"}},
				},
			},
			expectedCount: 0,
		},
		{
			name: "empty metadata",
			response: DQLQueryResponse{
				State:    "SUCCEEDED",
				Metadata: &DQLMetadata{},
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifications := tt.response.GetNotifications()
			if len(notifications) != tt.expectedCount {
				t.Errorf("expected %d notifications, got %d", tt.expectedCount, len(notifications))
			}
			if tt.expectedCount > 0 {
				if notifications[0].Severity != tt.expectedSeverity {
					t.Errorf("expected severity %s, got %s", tt.expectedSeverity, notifications[0].Severity)
				}
				if notifications[0].Message != tt.expectedMessage {
					t.Errorf("expected message %s, got %s", tt.expectedMessage, notifications[0].Message)
				}
			}
		})
	}
}

func TestDQLQueryResponse_ParseNotificationsFromJSON(t *testing.T) {
	// Test parsing the actual JSON format from the API response
	jsonResponse := `{
		"state": "SUCCEEDED",
		"progress": 100,
		"result": {
			"records": [{"count()": "194414758"}],
			"types": [{"indexRange": [0, 0], "mappings": {"count()": {"type": "long"}}}]
		},
		"metadata": {
			"grail": {
				"canonicalQuery": "fetch spans, from:-10d\n| summarize count()",
				"timezone": "Z",
				"query": "fetch spans, from: -10d | summarize count()",
				"scannedRecords": 268132936,
				"dqlVersion": "V1_0",
				"scannedBytes": 500000000000,
				"scannedDataPoints": 0,
				"executionTimeMilliseconds": 1676,
				"notifications": [
					{
						"severity": "WARNING",
						"messageFormat": "Your execution was stopped after %1$s gigabytes of data were scanned.",
						"arguments": ["500"],
						"notificationType": "SCAN_LIMIT_GBYTES",
						"message": "Your execution was stopped after 500 gigabytes of data were scanned."
					}
				],
				"queryId": "fe5b87f0-dfe8-457d-899b-50eabf9ab55d",
				"sampled": false
			}
		}
	}`

	var response DQLQueryResponse
	if err := json.Unmarshal([]byte(jsonResponse), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	notifications := response.GetNotifications()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}

	n := notifications[0]
	if n.Severity != "WARNING" {
		t.Errorf("expected severity WARNING, got %s", n.Severity)
	}
	if n.NotificationType != "SCAN_LIMIT_GBYTES" {
		t.Errorf("expected notificationType SCAN_LIMIT_GBYTES, got %s", n.NotificationType)
	}
	if n.Message == "" {
		t.Error("expected non-empty message")
	}
	if len(n.Arguments) != 1 || n.Arguments[0] != "500" {
		t.Errorf("expected arguments [500], got %v", n.Arguments)
	}

	// Also verify metadata fields were parsed
	if response.Metadata.Grail.ScannedBytes != 500000000000 {
		t.Errorf("expected scannedBytes 500000000000, got %d", response.Metadata.Grail.ScannedBytes)
	}
	if response.Metadata.Grail.ExecutionTimeMilliseconds != 1676 {
		t.Errorf("expected executionTimeMilliseconds 1676, got %d", response.Metadata.Grail.ExecutionTimeMilliseconds)
	}
}

func TestGetHintForNotification(t *testing.T) {
	tests := []struct {
		name             string
		notificationType string
		message          string
		wantHint         bool
		wantContains     string
	}{
		{
			name:             "scan limit by type",
			notificationType: "SCAN_LIMIT_GBYTES",
			message:          "",
			wantHint:         true,
			wantContains:     "--default-scan-limit-gbytes",
		},
		{
			name:             "result limit records by type",
			notificationType: "RESULT_LIMIT_RECORDS",
			message:          "",
			wantHint:         true,
			wantContains:     "--max-result-records",
		},
		{
			name:             "result limit bytes by type",
			notificationType: "RESULT_LIMIT_BYTES",
			message:          "",
			wantHint:         true,
			wantContains:     "--max-result-bytes",
		},
		{
			name:             "fetch timeout by type",
			notificationType: "FETCH_TIMEOUT",
			message:          "",
			wantHint:         true,
			wantContains:     "--fetch-timeout-seconds",
		},
		{
			name:             "sampling applied by type",
			notificationType: "SAMPLING_APPLIED",
			message:          "",
			wantHint:         true,
			wantContains:     "--default-sampling-ratio",
		},
		{
			name:             "consumption limit by type",
			notificationType: "QUERY_CONSUMPTION_LIMIT",
			message:          "",
			wantHint:         true,
			wantContains:     "--enforce-query-consumption-limit",
		},
		{
			name:             "result limited by message pattern",
			notificationType: "",
			message:          "Result has been limited to 1000 records",
			wantHint:         true,
			wantContains:     "--max-result-records",
		},
		{
			name:             "scan gigabyte by message pattern",
			notificationType: "",
			message:          "Scan stopped after 500 gigabytes were processed",
			wantHint:         true,
			wantContains:     "--default-scan-limit-gbytes",
		},
		{
			name:             "unknown notification type",
			notificationType: "UNKNOWN_TYPE",
			message:          "",
			wantHint:         false,
		},
		{
			name:             "empty notification",
			notificationType: "",
			message:          "",
			wantHint:         false,
		},
		{
			name:             "unrelated message",
			notificationType: "",
			message:          "Query completed successfully",
			wantHint:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := getHintForNotification(tt.notificationType, tt.message)

			if tt.wantHint {
				if hint == "" {
					t.Error("expected a hint, got empty string")
				}
				if tt.wantContains != "" && !strings.Contains(hint, tt.wantContains) {
					t.Errorf("hint %q should contain %q", hint, tt.wantContains)
				}
			} else if hint != "" {
				t.Errorf("expected no hint, got %q", hint)
			}
		})
	}
}

func TestClassifyNotification(t *testing.T) {
	tests := []struct {
		name             string
		notificationType string
		message          string
		want             string
	}{
		{"scan by type", "SCAN_LIMIT_GBYTES", "", notifScanLimit},
		{"result records by type", "RESULT_LIMIT_RECORDS", "", notifResultLimit},
		{"result bytes by type", "RESULT_LIMIT_BYTES", "", notifResultLimit},
		{"timeout by type", "FETCH_TIMEOUT", "", notifTimeout},
		{"sampling by type", "SAMPLING_APPLIED", "", notifSampling},
		{"consumption by type", "QUERY_CONSUMPTION_LIMIT", "", notifConsumption},
		{"scan by message", "", "stopped after 500 gigabytes were scanned", notifScanLimit},
		{"result by message", "", "Your result has been limited to 1000.", notifResultLimit},
		{"unknown", "SOMETHING_ELSE", "all good", ""},
		{"empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyNotification(tt.notificationType, tt.message); got != tt.want {
				t.Errorf("classifyNotification(%q, %q) = %q, want %q", tt.notificationType, tt.message, got, tt.want)
			}
		})
	}
}

func TestResultIsPartial(t *testing.T) {
	partial := []QueryNotification{
		{NotificationType: "RESULT_LIMIT_RECORDS"},
		{NotificationType: "RESULT_LIMIT_BYTES"},
		{NotificationType: "SCAN_LIMIT_GBYTES"},
		{NotificationType: "FETCH_TIMEOUT"},
		{NotificationType: "QUERY_CONSUMPTION_LIMIT"},
		{Message: "Your result has been limited to 1000."},
	}
	for _, n := range partial {
		if !ResultIsPartial(n) {
			t.Errorf("ResultIsPartial(%+v) = false, want true", n)
		}
	}
	// Sampling is a declared query property, not a silent truncation.
	for _, n := range []QueryNotification{{NotificationType: "SAMPLING_APPLIED"}, {Message: "all good"}} {
		if ResultIsPartial(n) {
			t.Errorf("ResultIsPartial(%+v) = true, want false", n)
		}
	}
}

func TestAgentAdviceForNotification_ScanLimit(t *testing.T) {
	got := agentAdviceForNotification("SCAN_LIMIT_GBYTES", "")
	if len(got) == 0 {
		t.Fatal("expected scan-limit advice, got none")
	}
	joined := strings.Join(got, "\n")
	// The result must be flagged as PARTIAL and reduction advice must come
	// before the (expensive) raise-the-limit escape hatch.
	if !strings.Contains(joined, "PARTIAL") {
		t.Errorf("scan-limit advice should flag the result as PARTIAL:\n%s", joined)
	}
	for _, want := range []string{"timeframe", "bucket", "contributions", "--default-sampling-ratio"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scan-limit advice missing %q:\n%s", want, joined)
		}
	}
	reduceIdx := strings.Index(joined, "--default-sampling-ratio")
	raiseIdx := strings.Index(joined, "--default-scan-limit-gbytes")
	if raiseIdx < 0 || reduceIdx < 0 || reduceIdx > raiseIdx {
		t.Errorf("reduction advice should precede the raise-the-limit hint:\n%s", joined)
	}
}

func TestNotificationAdvice(t *testing.T) {
	notifications := []QueryNotification{
		{Severity: "WARNING", NotificationType: "SCAN_LIMIT_GBYTES", Message: "stopped after 500 gigabytes"},
		{Severity: "INFO", NotificationType: "RESULT_LIMIT_RECORDS", Message: "informational, ignore me"},
	}
	warnings, suggestions := notificationAdvice(notifications)
	if len(warnings) != 1 || warnings[0] != "stopped after 500 gigabytes" {
		t.Errorf("expected only the WARNING message surfaced, got %v", warnings)
	}
	if len(suggestions) == 0 || !strings.Contains(strings.Join(suggestions, "\n"), "PARTIAL") {
		t.Errorf("expected scan-limit suggestions, got %v", suggestions)
	}
	// The INFO notification must not contribute advice.
	if strings.Contains(strings.Join(suggestions, "\n"), "record/byte limit") {
		t.Errorf("INFO-severity notification should be ignored, got %v", suggestions)
	}
}

func TestNewDQLExecutor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	if executor == nil {
		t.Fatal("NewDQLExecutor returned nil")
	}
	if executor.client != c {
		t.Error("executor client not set correctly")
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_RunningNoToken(t *testing.T) {
	// Test error when query is running but no request token is returned
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := DQLQueryResponse{
			State:        "RUNNING",
			RequestToken: "", // Missing token
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	_, err = executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})

	if err == nil {
		t.Fatal("expected error for missing request token")
	}
	if !strings.Contains(err.Error(), "no request token") {
		t.Errorf("expected 'no request token' error, got: %v", err)
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_PollFailed(t *testing.T) {
	// Test handling of FAILED state during polling
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			response := DQLQueryResponse{
				State:        "RUNNING",
				RequestToken: "test-token-123",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/platform/storage/query/v1/query:poll" {
			response := DQLQueryResponse{
				State: "FAILED",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	_, err = executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})

	if err == nil {
		t.Fatal("expected error for failed query")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("expected 'failed' in error, got: %v", err)
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_AllParameters(t *testing.T) {
	// Test that all DQL API parameters are correctly sent in the request body
	var receivedRequest DQLQueryRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the request body
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		response := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{
					{"test": "value"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	// Test with all parameters set
	opts := DQLExecuteOptions{
		MaxResultRecords:             5000,
		MaxResultBytes:               10000000,
		DefaultScanLimitGbytes:       5.0,
		DefaultSamplingRatio:         10.0,
		FetchTimeoutSeconds:          120,
		EnablePreview:                true,
		EnforceQueryConsumptionLimit: true,
		IncludeTypes:                 true,
		IncludeContributions:         true,
		DefaultTimeframeStart:        "2022-04-20T12:10:04.123Z",
		DefaultTimeframeEnd:          "2022-04-20T13:10:04.123Z",
		Locale:                       "en_US",
		Timezone:                     "Europe/Paris",
	}

	_, err = executor.ExecuteQueryWithOptions("fetch logs", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all parameters were sent correctly
	if receivedRequest.MaxResultRecords != 5000 {
		t.Errorf("expected MaxResultRecords to be 5000, got %d", receivedRequest.MaxResultRecords)
	}
	if receivedRequest.MaxResultBytes != 10000000 {
		t.Errorf("expected MaxResultBytes to be 10000000, got %d", receivedRequest.MaxResultBytes)
	}
	if receivedRequest.DefaultScanLimitGbytes != 5.0 {
		t.Errorf("expected DefaultScanLimitGbytes to be 5.0, got %f", receivedRequest.DefaultScanLimitGbytes)
	}
	if receivedRequest.DefaultSamplingRatio != 10.0 {
		t.Errorf("expected DefaultSamplingRatio to be 10.0, got %f", receivedRequest.DefaultSamplingRatio)
	}
	if receivedRequest.FetchTimeoutSeconds != 120 {
		t.Errorf("expected FetchTimeoutSeconds to be 120, got %d", receivedRequest.FetchTimeoutSeconds)
	}
	if receivedRequest.EnablePreview != true {
		t.Errorf("expected EnablePreview to be true, got %v", receivedRequest.EnablePreview)
	}
	if receivedRequest.EnforceQueryConsumptionLimit != true {
		t.Errorf("expected EnforceQueryConsumptionLimit to be true, got %v", receivedRequest.EnforceQueryConsumptionLimit)
	}
	if receivedRequest.IncludeTypes == nil || *receivedRequest.IncludeTypes != true {
		t.Errorf("expected IncludeTypes to be true, got %v", receivedRequest.IncludeTypes)
	}
	if receivedRequest.IncludeContributions == nil || *receivedRequest.IncludeContributions != true {
		t.Errorf("expected IncludeContributions to be true, got %v", receivedRequest.IncludeContributions)
	}
	if receivedRequest.DefaultTimeframeStart != "2022-04-20T12:10:04.123Z" {
		t.Errorf("expected DefaultTimeframeStart to be '2022-04-20T12:10:04.123Z', got %s", receivedRequest.DefaultTimeframeStart)
	}
	if receivedRequest.DefaultTimeframeEnd != "2022-04-20T13:10:04.123Z" {
		t.Errorf("expected DefaultTimeframeEnd to be '2022-04-20T13:10:04.123Z', got %s", receivedRequest.DefaultTimeframeEnd)
	}
	if receivedRequest.Locale != "en_US" {
		t.Errorf("expected Locale to be 'en_US', got %s", receivedRequest.Locale)
	}
	if receivedRequest.Timezone != "Europe/Paris" {
		t.Errorf("expected Timezone to be 'Europe/Paris', got %s", receivedRequest.Timezone)
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_IncludeContributions_OmittedWhenFalse(t *testing.T) {
	// When IncludeContributions is false (default), the field should be omitted
	// from the JSON request body (nil pointer + omitempty), not sent as false.
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		response := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{
					{"test": "value"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	// Execute with default options (IncludeContributions = false)
	opts := DQLExecuteOptions{}
	_, err = executor.ExecuteQueryWithOptions("fetch logs", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bodyStr := string(receivedBody)
	if strings.Contains(bodyStr, "includeContributions") {
		t.Errorf("expected includeContributions to be omitted from request body when false, but found it in: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "includeTypes") {
		t.Errorf("expected includeTypes to be omitted from request body when false, but found it in: %s", bodyStr)
	}
}

func TestDQLExecutor_ExecuteQueryWithOptions_IncludeContributions_SentWhenTrue(t *testing.T) {
	// When IncludeContributions is true, the field should be sent as true in the request body.
	var receivedRequest DQLQueryRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		response := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{
					{"test": "value"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	opts := DQLExecuteOptions{
		IncludeContributions: true,
	}
	_, err = executor.ExecuteQueryWithOptions("fetch logs", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedRequest.IncludeContributions == nil {
		t.Fatal("expected IncludeContributions to be set, got nil")
	}
	if *receivedRequest.IncludeContributions != true {
		t.Errorf("expected IncludeContributions to be true, got %v", *receivedRequest.IncludeContributions)
	}
}

// TestVerifyQuery_Valid tests verification of a valid query
func TestVerifyQuery_Valid(t *testing.T) {
	var receivedRequest DQLVerifyRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify correct endpoint
		if r.URL.Path != "/platform/storage/query/v1/query:verify" {
			t.Errorf("expected path /platform/storage/query/v1/query:verify, got %s", r.URL.Path)
		}

		// Capture request body
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		// Return valid response
		response := DQLVerifyResponse{
			Valid: true,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	result, err := executor.VerifyQuery("fetch logs", DQLVerifyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Valid {
		t.Error("expected valid query")
	}

	if receivedRequest.Query != "fetch logs" {
		t.Errorf("expected query 'fetch logs', got %s", receivedRequest.Query)
	}
}

// TestVerifyQuery_Invalid tests verification of an invalid query
func TestVerifyQuery_Invalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return invalid response with syntax error notification
		response := DQLVerifyResponse{
			Valid: false,
			Notifications: []MetadataNotification{
				{
					Severity:         "ERROR",
					NotificationType: "SYNTAX_ERROR",
					Message:          "Expected command, got 'invalid'",
					SyntaxPosition: &SyntaxPosition{
						Start: &Position{Line: 1, Column: 1},
						End:   &Position{Line: 1, Column: 8},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	result, err := executor.VerifyQuery("invalid query", DQLVerifyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid query")
	}

	if len(result.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(result.Notifications))
	}

	n := result.Notifications[0]
	if n.Severity != "ERROR" {
		t.Errorf("expected severity ERROR, got %s", n.Severity)
	}
	if n.NotificationType != "SYNTAX_ERROR" {
		t.Errorf("expected notificationType SYNTAX_ERROR, got %s", n.NotificationType)
	}
	if n.Message == "" {
		t.Error("expected non-empty error message")
	}
	if n.SyntaxPosition == nil {
		t.Error("expected syntax position for syntax error")
	} else if n.SyntaxPosition.Start == nil || n.SyntaxPosition.Start.Line != 1 {
		t.Error("expected syntax position with line 1")
	}
}

// TestVerifyQuery_WithCanonical tests verification with canonical query generation
func TestVerifyQuery_WithCanonical(t *testing.T) {
	var receivedRequest DQLVerifyRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		response := DQLVerifyResponse{
			Valid:          true,
			CanonicalQuery: "fetch logs\n| limit 100",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	opts := DQLVerifyOptions{
		GenerateCanonicalQuery: true,
	}

	result, err := executor.VerifyQuery("fetch logs | limit 100", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Valid {
		t.Error("expected valid query")
	}

	if result.CanonicalQuery == "" {
		t.Error("expected canonical query to be returned")
	}

	if receivedRequest.GenerateCanonicalQuery != true {
		t.Error("expected GenerateCanonicalQuery to be true in request")
	}

	expectedCanonical := "fetch logs\n| limit 100"
	if result.CanonicalQuery != expectedCanonical {
		t.Errorf("expected canonical query %q, got %q", expectedCanonical, result.CanonicalQuery)
	}
}

// TestVerifyQuery_WithTimezone tests verification with timezone option
func TestVerifyQuery_WithTimezone(t *testing.T) {
	var receivedRequest DQLVerifyRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		response := DQLVerifyResponse{
			Valid: true,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	opts := DQLVerifyOptions{
		Timezone: "Europe/Paris",
	}

	result, err := executor.VerifyQuery("fetch logs", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Valid {
		t.Error("expected valid query")
	}

	if receivedRequest.Timezone != "Europe/Paris" {
		t.Errorf("expected timezone 'Europe/Paris', got %s", receivedRequest.Timezone)
	}
}

// TestVerifyQuery_WithLocale tests verification with locale option
func TestVerifyQuery_WithLocale(t *testing.T) {
	var receivedRequest DQLVerifyRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		response := DQLVerifyResponse{
			Valid: true,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	opts := DQLVerifyOptions{
		Locale: "en_US",
	}

	result, err := executor.VerifyQuery("fetch logs", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Valid {
		t.Error("expected valid query")
	}

	if receivedRequest.Locale != "en_US" {
		t.Errorf("expected locale 'en_US', got %s", receivedRequest.Locale)
	}
}

// TestVerifyQuery_WithAllOptions tests verification with all options
func TestVerifyQuery_WithAllOptions(t *testing.T) {
	var receivedRequest DQLVerifyRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedRequest)

		response := DQLVerifyResponse{
			Valid:          true,
			CanonicalQuery: "fetch logs\n| limit 100",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	opts := DQLVerifyOptions{
		GenerateCanonicalQuery: true,
		Timezone:               "America/New_York",
		Locale:                 "de_DE",
	}

	result, err := executor.VerifyQuery("fetch logs | limit 100", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Valid {
		t.Error("expected valid query")
	}

	// Verify all request parameters
	if receivedRequest.Query != "fetch logs | limit 100" {
		t.Errorf("expected query 'fetch logs | limit 100', got %s", receivedRequest.Query)
	}
	if receivedRequest.GenerateCanonicalQuery != true {
		t.Error("expected GenerateCanonicalQuery to be true")
	}
	if receivedRequest.Timezone != "America/New_York" {
		t.Errorf("expected timezone 'America/New_York', got %s", receivedRequest.Timezone)
	}
	if receivedRequest.Locale != "de_DE" {
		t.Errorf("expected locale 'de_DE', got %s", receivedRequest.Locale)
	}

	// Verify canonical query in response
	if result.CanonicalQuery == "" {
		t.Error("expected canonical query to be returned")
	}
}

// TestVerifyQuery_NetworkError tests handling of network errors
func TestVerifyQuery_NetworkError(t *testing.T) {
	// Create client pointing to non-existent server
	c, err := client.NewForTesting("http://localhost:0", "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	_, err = executor.VerifyQuery("fetch logs", DQLVerifyOptions{})
	if err == nil {
		t.Fatal("expected network error, got nil")
	}

	if !strings.Contains(err.Error(), "failed to verify query") {
		t.Errorf("expected 'failed to verify query' error, got: %v", err)
	}
}

// TestVerifyQuery_AuthError tests handling of authentication errors
func TestVerifyQuery_AuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 401 Unauthorized
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"code": 401, "message": "Unauthorized"}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	_, err = executor.VerifyQuery("fetch logs", DQLVerifyOptions{})
	if err == nil {
		t.Fatal("expected authentication error, got nil")
	}

	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected '401' in error, got: %v", err)
	}
}

// TestVerifyQuery_ServerError tests handling of server errors
func TestVerifyQuery_ServerError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		expectedErrMsg string
	}{
		{
			name:           "bad request",
			statusCode:     http.StatusBadRequest,
			expectedErrMsg: "query verification failed",
		},
		{
			name:           "internal server error",
			statusCode:     http.StatusInternalServerError,
			expectedErrMsg: "query verification failed",
		},
		{
			name:           "service unavailable",
			statusCode:     http.StatusServiceUnavailable,
			expectedErrMsg: "query verification failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte("Error"))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			executor := NewDQLExecutor(c)

			_, err = executor.VerifyQuery("fetch logs", DQLVerifyOptions{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !strings.Contains(err.Error(), tt.expectedErrMsg) {
				t.Errorf("expected error containing '%s', got '%s'", tt.expectedErrMsg, err.Error())
			}
		})
	}
}

// TestVerifyTypes_JSONMarshaling tests JSON marshaling/unmarshaling of verify types
func TestVerifyTypes_JSONMarshaling(t *testing.T) {
	t.Run("DQLVerifyRequest", func(t *testing.T) {
		req := DQLVerifyRequest{
			Query:                  "fetch logs",
			GenerateCanonicalQuery: true,
			Timezone:               "UTC",
			Locale:                 "en_US",
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded DQLVerifyRequest
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.Query != req.Query {
			t.Errorf("expected query %s, got %s", req.Query, decoded.Query)
		}
		if decoded.GenerateCanonicalQuery != req.GenerateCanonicalQuery {
			t.Errorf("expected GenerateCanonicalQuery %v, got %v", req.GenerateCanonicalQuery, decoded.GenerateCanonicalQuery)
		}
		if decoded.Timezone != req.Timezone {
			t.Errorf("expected timezone %s, got %s", req.Timezone, decoded.Timezone)
		}
		if decoded.Locale != req.Locale {
			t.Errorf("expected locale %s, got %s", req.Locale, decoded.Locale)
		}
	})

	t.Run("DQLVerifyResponse", func(t *testing.T) {
		resp := DQLVerifyResponse{
			Valid:          false,
			CanonicalQuery: "fetch logs\n| limit 100",
			Notifications: []MetadataNotification{
				{
					Severity:         "ERROR",
					NotificationType: "SYNTAX_ERROR",
					Message:          "Test error",
					SyntaxPosition: &SyntaxPosition{
						Start: &Position{Line: 1, Column: 5},
						End:   &Position{Line: 1, Column: 10},
					},
				},
			},
		}

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded DQLVerifyResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.Valid != resp.Valid {
			t.Errorf("expected valid %v, got %v", resp.Valid, decoded.Valid)
		}
		if decoded.CanonicalQuery != resp.CanonicalQuery {
			t.Errorf("expected canonical query %s, got %s", resp.CanonicalQuery, decoded.CanonicalQuery)
		}
		if len(decoded.Notifications) != len(resp.Notifications) {
			t.Fatalf("expected %d notifications, got %d", len(resp.Notifications), len(decoded.Notifications))
		}

		n := decoded.Notifications[0]
		if n.Severity != "ERROR" {
			t.Errorf("expected severity ERROR, got %s", n.Severity)
		}
		if n.NotificationType != "SYNTAX_ERROR" {
			t.Errorf("expected notificationType SYNTAX_ERROR, got %s", n.NotificationType)
		}
		if n.SyntaxPosition == nil {
			t.Fatal("expected syntax position")
		}
		if n.SyntaxPosition.Start.Line != 1 || n.SyntaxPosition.Start.Column != 5 {
			t.Errorf("expected start position (1,5), got (%d,%d)", n.SyntaxPosition.Start.Line, n.SyntaxPosition.Start.Column)
		}
	})

	t.Run("MetadataNotification", func(t *testing.T) {
		notif := MetadataNotification{
			Severity:         "WARNING",
			NotificationType: "DEPRECATION",
			Message:          "This feature is deprecated",
		}

		data, err := json.Marshal(notif)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded MetadataNotification
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.Severity != notif.Severity {
			t.Errorf("expected severity %s, got %s", notif.Severity, decoded.Severity)
		}
		if decoded.NotificationType != notif.NotificationType {
			t.Errorf("expected notificationType %s, got %s", notif.NotificationType, decoded.NotificationType)
		}
		if decoded.Message != notif.Message {
			t.Errorf("expected message %s, got %s", notif.Message, decoded.Message)
		}
	})

	t.Run("SyntaxPosition", func(t *testing.T) {
		pos := SyntaxPosition{
			Start: &Position{Line: 2, Column: 10},
			End:   &Position{Line: 2, Column: 20},
		}

		data, err := json.Marshal(pos)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded SyntaxPosition
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.Start == nil || decoded.End == nil {
			t.Fatal("expected start and end positions")
		}
		if decoded.Start.Line != 2 || decoded.Start.Column != 10 {
			t.Errorf("expected start (2,10), got (%d,%d)", decoded.Start.Line, decoded.Start.Column)
		}
		if decoded.End.Line != 2 || decoded.End.Column != 20 {
			t.Errorf("expected end (2,20), got (%d,%d)", decoded.End.Line, decoded.End.Column)
		}
	})
}

// TestVerifyQuery_ParseActualAPIResponse tests parsing real API response format
func TestVerifyQuery_ParseActualAPIResponse(t *testing.T) {
	// Test with actual JSON format from the verify API
	jsonResponse := `{
		"valid": false,
		"notifications": [
			{
				"severity": "ERROR",
				"notificationType": "SYNTAX_ERROR",
				"message": "Expected command, got 'invalid'",
				"syntaxPosition": {
					"start": {"line": 1, "column": 1},
					"end": {"line": 1, "column": 8}
				}
			},
			{
				"severity": "WARNING",
				"notificationType": "DEPRECATION_WARNING",
				"message": "This syntax is deprecated"
			}
		],
		"canonicalQuery": "fetch logs"
	}`

	var response DQLVerifyResponse
	if err := json.Unmarshal([]byte(jsonResponse), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if response.Valid {
		t.Error("expected valid to be false")
	}

	if len(response.Notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(response.Notifications))
	}

	// Check first notification (ERROR with syntax position)
	n1 := response.Notifications[0]
	if n1.Severity != "ERROR" {
		t.Errorf("expected severity ERROR, got %s", n1.Severity)
	}
	if n1.NotificationType != "SYNTAX_ERROR" {
		t.Errorf("expected notificationType SYNTAX_ERROR, got %s", n1.NotificationType)
	}
	if n1.SyntaxPosition == nil {
		t.Fatal("expected syntax position for first notification")
	}
	if n1.SyntaxPosition.Start.Line != 1 || n1.SyntaxPosition.Start.Column != 1 {
		t.Errorf("expected start position (1,1), got (%d,%d)", n1.SyntaxPosition.Start.Line, n1.SyntaxPosition.Start.Column)
	}

	// Check second notification (WARNING without syntax position)
	n2 := response.Notifications[1]
	if n2.Severity != "WARNING" {
		t.Errorf("expected severity WARNING, got %s", n2.Severity)
	}
	if n2.NotificationType != "DEPRECATION_WARNING" {
		t.Errorf("expected notificationType DEPRECATION_WARNING, got %s", n2.NotificationType)
	}
	if n2.SyntaxPosition != nil {
		t.Error("expected no syntax position for second notification")
	}

	if response.CanonicalQuery != "fetch logs" {
		t.Errorf("expected canonical query 'fetch logs', got %s", response.CanonicalQuery)
	}
}

func TestExtractQueryMetadata_FromResultMetadata(t *testing.T) {
	resp := &DQLQueryResponse{
		State: "SUCCEEDED",
		Result: &DQLResult{
			Records: []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z"}},
			Metadata: &DQLMetadata{
				Grail: &GrailMetadata{
					Query:                     "fetch logs | limit 3",
					CanonicalQuery:            "fetch logs\n| limit 3",
					QueryID:                   "test-id-123",
					DQLVersion:                "V1_0",
					Timezone:                  "Z",
					Locale:                    "und",
					ExecutionTimeMilliseconds: 47,
					ScannedRecords:            42351,
					ScannedBytes:              2982690,
					ScannedDataPoints:         0,
					Sampled:                   false,
					AnalysisTimeframe: &AnalysisTimeframe{
						Start: "2026-03-09T10:16:39Z",
						End:   "2026-03-09T12:16:39Z",
					},
					Contributions: &Contributions{
						Buckets: []BucketContribution{
							{
								Name:                "test_bucket",
								Table:               "logs",
								ScannedBytes:        2982690,
								MatchedRecordsRatio: 1.0,
							},
						},
					},
				},
			},
		},
	}

	meta := extractQueryMetadata(resp)
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}

	if meta.QueryID != "test-id-123" {
		t.Errorf("expected QueryID=test-id-123, got %q", meta.QueryID)
	}
	if meta.ExecutionTimeMilliseconds != 47 {
		t.Errorf("expected ExecutionTimeMilliseconds=47, got %d", meta.ExecutionTimeMilliseconds)
	}
	if meta.ScannedRecords != 42351 {
		t.Errorf("expected ScannedRecords=42351, got %d", meta.ScannedRecords)
	}
	if meta.ScannedBytes != 2982690 {
		t.Errorf("expected ScannedBytes=2982690, got %d", meta.ScannedBytes)
	}
	if meta.AnalysisTimeframe == nil {
		t.Fatal("expected AnalysisTimeframe, got nil")
	}
	if meta.AnalysisTimeframe.Start != "2026-03-09T10:16:39Z" {
		t.Errorf("expected AnalysisTimeframe.Start=2026-03-09T10:16:39Z, got %q", meta.AnalysisTimeframe.Start)
	}
	if meta.Contributions == nil {
		t.Fatal("expected Contributions, got nil")
	}
	if len(meta.Contributions.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(meta.Contributions.Buckets))
	}
	if meta.Contributions.Buckets[0].Name != "test_bucket" {
		t.Errorf("expected bucket name=test_bucket, got %q", meta.Contributions.Buckets[0].Name)
	}
}

func TestExtractQueryMetadata_FromTopLevelMetadata(t *testing.T) {
	resp := &DQLQueryResponse{
		State:   "SUCCEEDED",
		Records: []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z"}},
		Metadata: &DQLMetadata{
			Grail: &GrailMetadata{
				QueryID:                   "top-level-id",
				ExecutionTimeMilliseconds: 100,
			},
		},
	}

	meta := extractQueryMetadata(resp)
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}
	if meta.QueryID != "top-level-id" {
		t.Errorf("expected QueryID=top-level-id, got %q", meta.QueryID)
	}
}

func TestExtractQueryMetadata_NilMetadata(t *testing.T) {
	resp := &DQLQueryResponse{
		State:   "SUCCEEDED",
		Records: []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z"}},
	}

	meta := extractQueryMetadata(resp)
	if meta != nil {
		t.Errorf("expected nil metadata, got %+v", meta)
	}
}

func TestExtractQueryMetadata_NilGrail(t *testing.T) {
	resp := &DQLQueryResponse{
		State:    "SUCCEEDED",
		Records:  []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z"}},
		Metadata: &DQLMetadata{Grail: nil},
	}

	meta := extractQueryMetadata(resp)
	if meta != nil {
		t.Errorf("expected nil metadata for nil Grail, got %+v", meta)
	}
}

// TestExtractQueryMetadata_NilMetadata_PrintPath verifies that when --metadata
// is requested but the API returns no metadata (nil), the output contains only
// records without a metadata key. This tests the nil guard in printResults().
func TestExtractQueryMetadata_NilMetadata_PrintPath(t *testing.T) {
	// Simulate the same logic as printResults() for the JSON/YAML default case:
	// 1. opts.MetadataFields is set (user passed --metadata)
	// 2. extractQueryMetadata returns nil (API has no metadata)
	// 3. The "metadata" key should NOT appear in the output

	resp := &DQLQueryResponse{
		State:   "SUCCEEDED",
		Records: []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z", "content": "test"}},
		// No Metadata field — simulates API response without metadata
	}

	meta := extractQueryMetadata(resp)
	if meta != nil {
		t.Fatal("expected extractQueryMetadata to return nil for response without metadata")
	}

	// Build the output map the same way printResults() does
	out := make(map[string]interface{})
	out["records"] = resp.Records
	if meta != nil {
		out["metadata"] = meta // This should NOT execute
	}

	jsonOut, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Verify "metadata" key is absent
	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonOut, &parsed); err != nil {
		t.Fatalf("could not parse JSON: %v", err)
	}
	if _, exists := parsed["metadata"]; exists {
		t.Error("output should NOT contain 'metadata' key when API returns no metadata")
	}
	if _, exists := parsed["records"]; !exists {
		t.Error("output should contain 'records' key")
	}
}

// TestExtractQueryMetadata_MetricsWithoutGrail verifies that Metrics (timeseries
// metric descriptors) are extracted even when Grail metadata is absent — the two
// are independent siblings of the response's metadata section, and a response
// with metadata.metrics but no metadata.grail must not have its metrics dropped.
func TestBuildExecuteRequest_EnrichMetricMetadataGating(t *testing.T) {
	cases := []struct {
		name   string
		fields []string
		want   bool
	}{
		{"no metadata", nil, false},
		{"unrelated field", []string{"scannedRecords"}, false},
		{"metrics selected", []string{"metrics"}, true},
		{"metrics among others", []string{"scannedRecords", "metrics"}, true},
		{"all selector", []string{"all"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := buildExecuteRequest("timeseries avg(dt.host.cpu.user)", DQLExecuteOptions{MetadataFields: tc.fields})
			if req.EnrichMetricMetadata != tc.want {
				t.Errorf("EnrichMetricMetadata = %v, want %v", req.EnrichMetricMetadata, tc.want)
			}
		})
	}
}

func TestExtractQueryMetadata_MetricsWithoutGrail(t *testing.T) {
	resp := &DQLQueryResponse{
		State:   "SUCCEEDED",
		Records: []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z"}},
		Metadata: &DQLMetadata{
			Grail: nil,
			Metrics: []MetricInfo{
				{MetricKey: "dt.host.cpu.usage", FieldName: "avg(dt.host.cpu.usage)", Aggregation: "avg", DisplayName: "CPU usage", Description: "Average CPU usage across all cores", Unit: "Percent"},
			},
		},
	}

	meta := extractQueryMetadata(resp)
	if meta == nil {
		t.Fatal("expected metadata for response with Metrics but no Grail, got nil")
	}
	if len(meta.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(meta.Metrics))
	}
	if meta.Metrics[0].MetricKey != "dt.host.cpu.usage" {
		t.Errorf("expected MetricKey=dt.host.cpu.usage, got %q", meta.Metrics[0].MetricKey)
	}
	// Descriptor fields (displayName/description/unit) must be carried through the conversion.
	if meta.Metrics[0].DisplayName != "CPU usage" {
		t.Errorf("expected DisplayName=CPU usage, got %q", meta.Metrics[0].DisplayName)
	}
	if meta.Metrics[0].Description != "Average CPU usage across all cores" {
		t.Errorf("expected Description to be carried through, got %q", meta.Metrics[0].Description)
	}
	if meta.Metrics[0].Unit != "Percent" {
		t.Errorf("expected Unit=Percent, got %q", meta.Metrics[0].Unit)
	}
	// Grail-derived fields must remain zero-valued since Grail was absent.
	if meta.QueryID != "" || meta.ExecutionTimeMilliseconds != 0 {
		t.Errorf("expected zero-valued Grail fields, got QueryID=%q ExecutionTimeMilliseconds=%d", meta.QueryID, meta.ExecutionTimeMilliseconds)
	}
}

func TestExtractQueryMetadata_ResultMetadataPrecedence(t *testing.T) {
	// When both result.Metadata and top-level Metadata exist,
	// result.Metadata should take precedence
	resp := &DQLQueryResponse{
		State: "SUCCEEDED",
		Result: &DQLResult{
			Records: []map[string]interface{}{{"timestamp": "2026-03-09T12:15:00Z"}},
			Metadata: &DQLMetadata{
				Grail: &GrailMetadata{
					QueryID: "result-level-id",
				},
			},
		},
		Metadata: &DQLMetadata{
			Grail: &GrailMetadata{
				QueryID: "top-level-id",
			},
		},
	}

	meta := extractQueryMetadata(resp)
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}
	if meta.QueryID != "result-level-id" {
		t.Errorf("expected result-level metadata to take precedence, got QueryID=%q", meta.QueryID)
	}
}

func TestDQLExecutor_Execute_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{
					{"host.name": "server-01", "dt.entity.host": "HOST-1"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	// Execute writes output to stdout/file — just verify no error
	err = executor.Execute("fetch hosts", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDQLExecutor_ExecuteWithOptions_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{{"event.name": "deploy"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	err = executor.ExecuteWithOptions("fetch events", DQLExecuteOptions{OutputFormat: "json"})
	if err != nil {
		t.Fatalf("ExecuteWithOptions() error = %v", err)
	}
}

func TestDQLExecutor_ExecuteWithOptions_QueryError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid query"}`)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	err = executor.ExecuteWithOptions("INVALID QUERY", DQLExecuteOptions{})
	if err == nil {
		t.Fatal("expected error for bad query, got nil")
	}
}

func TestDQLExecutor_PrintNotifications_Warning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	executor := NewDQLExecutor(c)

	// Should not panic or error — output goes to os.Stderr
	notifications := []QueryNotification{
		{Severity: "WARNING", Message: "scan limit reached", NotificationType: "SCAN_LIMIT"},
		{Severity: "ERROR", Message: "result limit reached", NotificationType: "RESULT_LIMIT_RECORDS"},
		{Severity: "INFO", Message: "info message"},
		{Message: "no severity set"},
	}
	executor.PrintNotifications(notifications) // Just verify no panic
}

func TestDQLExecutor_PrintNotifications_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	executor := NewDQLExecutor(c)
	executor.PrintNotifications(nil)                   // nil — should be no-op
	executor.PrintNotifications([]QueryNotification{}) // empty — should be no-op
}

// TestDQLExecutor_PollTokenRefresh verifies that a 401 during polling triggers a token refresh and retries.
func TestDQLExecutor_PollTokenRefresh(t *testing.T) {
	pollCallCount := 0
	refreshCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-abc"}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/platform/storage/query/v1/query:poll" {
			pollCallCount++
			if pollCallCount == 1 {
				// First poll: simulate expired JWT
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
				return
			}
			// Second poll (after refresh): succeed
			resp := DQLQueryResponse{
				State:  "SUCCEEDED",
				Result: &DQLResult{Records: []map[string]interface{}{{"key": "value"}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "old-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c).WithTokenRefresher(func() (string, error) {
		refreshCallCount++
		return "new-token", nil
	})

	result, err := executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("expected state SUCCEEDED, got %s", result.State)
	}
	if refreshCallCount != 1 {
		t.Errorf("expected token refresh to be called once, got %d", refreshCallCount)
	}
	if pollCallCount != 2 {
		t.Errorf("expected 2 poll calls (1 expired + 1 success), got %d", pollCallCount)
	}
}

// TestDQLExecutor_PollTokenRefresh_FailsWhenRefresherErrors verifies that a refresh error aborts polling.
func TestDQLExecutor_PollTokenRefresh_FailsWhenRefresherErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-abc"}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Always return 401
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "old-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c).WithTokenRefresher(func() (string, error) {
		return "", fmt.Errorf("refresh service unavailable")
	})

	_, err = executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "token refresh failed") {
		t.Errorf("expected error to mention token refresh failure, got: %v", err)
	}
}

// TestDQLExecutor_PollTokenRefresh_NoRefresherReturnsError verifies that without a refresher a 401 is a hard error.
func TestDQLExecutor_PollTokenRefresh_NoRefresherReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-abc"}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "old-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c) // no token refresher

	_, err = executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain status 401, got: %v", err)
	}
}

// TestDQLExecutor_CancelDuringPoll verifies that cancelling the context during polling
// returns a context.Canceled error and sends a cancel request to the backend.
func TestDQLExecutor_CancelDuringPoll(t *testing.T) {
	cancelCalled := false
	cancelToken := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-cancel-test"}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(resp)

		case "/platform/storage/query/v1/query:poll":
			// Always return RUNNING so the poll loop keeps going
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-cancel-test"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case "/platform/storage/query/v1/query:cancel":
			cancelCalled = true
			cancelToken = r.URL.Query().Get("request-token")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay to let at least one poll round trip
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result, err := executor.ExecuteQueryWithContext(ctx, "fetch logs", DQLExecuteOptions{})
	if err != nil {
		t.Fatalf("expected nil error on cancellation (clean exit), got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result on cancellation, got: %+v", result)
	}

	if !cancelCalled {
		t.Error("expected cancel API to be called after context cancellation")
	}
	if cancelToken != "tok-cancel-test" {
		t.Errorf("cancel called with wrong token: %q, want %q", cancelToken, "tok-cancel-test")
	}
}

// TestDQLExecutor_CancelWhilePollInFlight covers the scenario where ctx is cancelled
// while a poll HTTP request is already in-flight and the poll response arrives after
// the cancellation.  The test uses two channels to enforce this ordering:
//
//  1. pollStarted  – closed by the handler once the poll request is received,
//     signalling to the test goroutine that the GET is in-flight.
//  2. releasePoll  – closed by the test goroutine after calling cancel(), letting
//     the handler write the RUNNING response and return.
//
// After the poll response is received (or the transport aborts it), the loop-top
// ctx.Done() guard must fire and query:cancel must be called with the correct token.
func TestDQLExecutor_CancelWhilePollInFlight(t *testing.T) {
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	cancelCalled := false
	cancelToken := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-inflight"}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(resp)

		case "/platform/storage/query/v1/query:poll":
			// Signal that the poll GET is in-flight, then hold the response
			// until the test goroutine has cancelled the context.
			close(pollStarted)
			<-releasePoll
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-inflight"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case "/platform/storage/query/v1/query:cancel":
			cancelCalled = true
			cancelToken = r.URL.Query().Get("request-token")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		result *DQLQueryResponse
		err    error
	}
	done := make(chan outcome, 1)

	executor := NewDQLExecutor(c)
	go func() {
		result, err := executor.ExecuteQueryWithContext(ctx, "fetch logs", DQLExecuteOptions{})
		done <- outcome{result, err}
	}()

	// Poll is now in-flight — cancel the context, then release the poll response.
	<-pollStarted
	cancel()
	close(releasePoll)

	o := <-done
	if o.err != nil {
		t.Fatalf("expected nil error after in-flight cancellation, got: %v", o.err)
	}
	if o.result != nil {
		t.Errorf("expected nil result after cancellation, got: %+v", o.result)
	}
	if !cancelCalled {
		t.Error("expected query:cancel to be called after in-flight poll was released")
	}
	if cancelToken != "tok-inflight" {
		t.Errorf("cancel token = %q, want %q", cancelToken, "tok-inflight")
	}
}

// TestDQLExecutor_NoCancelOnImmediateSuccess verifies that when a query succeeds
// immediately (no polling), the cancel endpoint is never called.
func TestDQLExecutor_NoCancelOnImmediateSuccess(t *testing.T) {
	cancelCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:cancel" {
			cancelCalled = true
		}
		resp := DQLQueryResponse{
			State:  "SUCCEEDED",
			Result: &DQLResult{Records: []map[string]interface{}{{"k": "v"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	result, err := executor.ExecuteQueryWithContext(context.Background(), "fetch logs", DQLExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("expected SUCCEEDED, got %s", result.State)
	}
	if cancelCalled {
		t.Error("cancel should not be called for an immediately successful query")
	}
}

// TestDQLExecutor_CancelQuery_BestEffort verifies that CancelQuery does not propagate
// errors when the server returns an error response.
func TestDQLExecutor_CancelQuery_BestEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	// Should not panic or return an error
	executor.CancelQuery("some-token")
}

// TestDQLExecutor_CancelQuery_EmptyToken verifies that CancelQuery is a no-op for empty tokens.
func TestDQLExecutor_CancelQuery_EmptyToken(t *testing.T) {
	cancelCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancelCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	executor.CancelQuery("") // should be a no-op

	if cancelCalled {
		t.Error("cancel API should not be called for an empty token")
	}
}

// TestDQLExecutor_CancelAfterExecute verifies the code path where ctx is cancelled
// after the initial query:execute POST returns (and a requestToken is available) but
// before any poll request is sent.  ExecuteQueryWithContext must call query:cancel with
// the correct token and return (nil, nil).
func TestDQLExecutor_CancelAfterExecute(t *testing.T) {
	cancelCalled := false
	cancelToken := ""

	// Declare ctx/cancel before the server so the handler closure can call cancel().
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			// Cancel ctx before writing the response body. The executor's HTTP call
			// uses an independent context (execCtx), so it still receives the response.
			// When ExecuteAndPollWithOptions checks ctx.Err() after Execute returns,
			// the cancellation is guaranteed to be visible via the happens-before chain:
			// cancel() → write body → (network) → read body → ctx.Err().
			cancel()
			resp := DQLQueryResponse{State: "RUNNING", RequestToken: "tok-after-execute"}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(resp)

		case "/platform/storage/query/v1/query:cancel":
			cancelCalled = true
			cancelToken = r.URL.Query().Get("request-token")
			w.WriteHeader(http.StatusOK)

		default:
			// query:poll must never be reached in this test
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	result, err := executor.ExecuteQueryWithContext(ctx, "fetch logs", DQLExecuteOptions{})
	if err != nil {
		t.Fatalf("expected nil error on post-execute cancellation, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result on cancellation, got: %+v", result)
	}
	if !cancelCalled {
		t.Error("expected query:cancel to be called after context cancellation")
	}
	if cancelToken != "tok-after-execute" {
		t.Errorf("cancel called with wrong token: %q, want %q", cancelToken, "tok-after-execute")
	}
}

// TestDQLExecutor_ExecuteQueryWithOptions_StillWorks verifies that the refactored
// ExecuteQueryWithOptions delegate still behaves correctly.
func TestDQLExecutor_ExecuteQueryWithOptions_StillWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DQLQueryResponse{
			State:  "SUCCEEDED",
			Result: &DQLResult{Records: []map[string]interface{}{{"k": "v"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	result, err := executor.ExecuteQueryWithOptions("fetch logs", DQLExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("expected SUCCEEDED, got %s", result.State)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// was written. printResults renders to os.Stdout, so this lets the integration
// test observe the bytes that would reach a real terminal/redirect.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// TestDQLExecutor_OutputFormats_Integration drives the full jsonl/parquet path
// end-to-end against a mock Grail server: request building (includeTypes) ->
// SDK execute -> a response carrying a DQL `types` block -> columnTypeMappings
// flatten -> printer dispatch -> rendered bytes on stdout. The isolated unit
// tests each cover one link; this proves the links are wired together.
func TestDQLExecutor_OutputFormats_Integration(t *testing.T) {
	var gotReq DQLQueryRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
		}
		resp := DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &DQLResult{
				Records: []map[string]interface{}{
					// "count" is a DQL long that the API delivers as a JSON string.
					{"host": "web-01", "count": "194414758"},
				},
				Types: []sdkquery.ColumnTypes{
					{IndexRange: []int{0, 0}, Mappings: map[string]sdkquery.ColumnType{
						"host":  {Type: "string"},
						"count": {Type: "long"},
					}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	executor := NewDQLExecutor(c)

	t.Run("parquet requests types and emits a valid Parquet container", func(t *testing.T) {
		gotReq = DQLQueryRequest{}
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "fetch logs",
				DQLExecuteOptions{OutputFormat: "parquet", IncludeTypes: true}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		if gotReq.IncludeTypes == nil || !*gotReq.IncludeTypes {
			t.Errorf("expected includeTypes:true in request body for parquet, got %v", gotReq.IncludeTypes)
		}
		// A valid Parquet file is framed by the "PAR1" magic at both ends.
		if !bytes.HasPrefix(out, []byte("PAR1")) || !bytes.HasSuffix(out, []byte("PAR1")) {
			t.Errorf("output is not a valid Parquet container (PAR1 magic missing), %d bytes", len(out))
		}
	})

	t.Run("jsonl emits one compact object per record", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "fetch logs",
				DQLExecuteOptions{OutputFormat: "jsonl"}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		line := strings.TrimSpace(string(out))
		if strings.Contains(line, "\n") {
			t.Errorf("expected a single line for a single record, got: %q", line)
		}
		if !strings.Contains(line, `"host":"web-01"`) || !strings.Contains(line, `"count":"194414758"`) {
			t.Errorf("unexpected jsonl line: %q", line)
		}
	})

	t.Run("typed casts the long to an unquoted number end-to-end", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "fetch logs",
				DQLExecuteOptions{OutputFormat: "jsonl", Typed: true}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		line := strings.TrimSpace(string(out))
		// With --typed the DQL long "count" must render as a bare JSON number, not
		// the API's string encoding; the string column stays quoted.
		if !strings.Contains(line, `"count":194414758`) {
			t.Errorf("expected unquoted count with --typed, got: %q", line)
		}
		if strings.Contains(line, `"count":"194414758"`) {
			t.Errorf("count is still string-encoded despite --typed: %q", line)
		}
		if !strings.Contains(line, `"host":"web-01"`) {
			t.Errorf("string column should be unchanged: %q", line)
		}
	})

	t.Run("EmitTypes surfaces a top-level types block in json", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "fetch logs",
				DQLExecuteOptions{OutputFormat: "json", IncludeTypes: true, EmitTypes: true}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("output is not valid JSON: %v — %s", err, out)
		}
		if _, ok := doc["records"]; !ok {
			t.Errorf("expected a records key alongside types, got: %s", out)
		}
		raw, ok := doc["types"]
		if !ok {
			t.Fatalf("expected a top-level types key with EmitTypes, got: %s", out)
		}
		// The block must preserve the raw API shape: [{indexRange, mappings}].
		var groups []sdkquery.ColumnTypes
		if err := json.Unmarshal(raw, &groups); err != nil {
			t.Fatalf("types block is not the expected shape: %v — %s", err, raw)
		}
		if len(groups) != 1 || groups[0].Mappings["count"].Type != "long" {
			t.Errorf("unexpected types block: %s", raw)
		}
	})

	t.Run("EmitTypes preserves API field names in yaml", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "fetch logs",
				DQLExecuteOptions{OutputFormat: "yaml", IncludeTypes: true, EmitTypes: true}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var doc map[string]interface{}
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatalf("output is not valid YAML: %v - %s", err, out)
		}
		groups, ok := doc["types"].([]interface{})
		if !ok || len(groups) != 1 {
			t.Fatalf("expected one type group, got: %#v", doc["types"])
		}
		group, ok := groups[0].(map[string]interface{})
		if !ok {
			t.Fatalf("type group is not a mapping: %#v", groups[0])
		}
		if _, ok := group["indexRange"]; !ok {
			t.Errorf("expected API field name indexRange, got: %s", out)
		}
		if _, ok := group["indexrange"]; ok {
			t.Errorf("unexpected lowercased field name indexrange: %s", out)
		}
	})

	t.Run("types block is omitted without an explicit --include-types", func(t *testing.T) {
		// --typed forces IncludeTypes on internally, but EmitTypes stays false, so
		// the block must not leak into output.
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "fetch logs",
				DQLExecuteOptions{OutputFormat: "json", Typed: true}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		if bytes.Contains(out, []byte(`"types"`)) {
			t.Errorf("types block leaked into output without --include-types: %s", out)
		}
	})
}

// parseDTClientContext unmarshals the dt-client-context header value into a map.
func parseDTClientContext(t *testing.T, raw string) map[string]string {
	t.Helper()
	if raw == "" {
		t.Fatal("dt-client-context header is empty")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("dt-client-context is not valid JSON: %v — value: %s", err, raw)
	}
	return m
}

// clearAIAgentEnvVars overrides every env var that aidetect checks to "0" so that
// agent detection is disabled for the duration of the test. This is necessary because
// the test process may itself be running inside an AI agent (e.g. Claude Code sets
// CLAUDECODE=1), which would otherwise bleed into tests that expect no agent field.
func clearAIAgentEnvVars(t *testing.T) {
	t.Helper()
	for _, env := range aidetect.EnvVars() {
		t.Setenv(env, "")
	}
}

func TestDTClientContextHeader_DefaultFields(t *testing.T) {
	clearAIAgentEnvVars(t)

	h := dtClientContextHeader("")
	m := parseDTClientContext(t, h)

	if m["app"] != "dtctl" {
		t.Errorf("app = %q, want %q", m["app"], "dtctl")
	}
	if m["version"] == "" {
		t.Error("version field must not be empty")
	}
	if _, hasCtx := m["context"]; hasCtx {
		t.Error("context field should be omitted when callerContext is empty")
	}
	if _, hasAgent := m["agent"]; hasAgent {
		t.Error("agent field should be omitted when no AI agent env var is set")
	}
}

func TestDTClientContextHeader_WithCallerContext(t *testing.T) {
	clearAIAgentEnvVars(t)

	h := dtClientContextHeader("root-cause-analysis")
	m := parseDTClientContext(t, h)

	if m["context"] != "root-cause-analysis" {
		t.Errorf("context = %q, want %q", m["context"], "root-cause-analysis")
	}
	if m["app"] != "dtctl" {
		t.Errorf("app = %q, want %q", m["app"], "dtctl")
	}
}

func TestDTClientContextHeader_WithAIAgent(t *testing.T) {
	clearAIAgentEnvVars(t)
	t.Setenv("AI_AGENT", "1") // triggers "generic-ai" in aidetect

	h := dtClientContextHeader("")
	m := parseDTClientContext(t, h)

	if m["agent"] != "generic-ai" {
		t.Errorf("agent = %q, want %q", m["agent"], "generic-ai")
	}
	if _, hasCtx := m["context"]; hasCtx {
		t.Error("context field should be omitted when callerContext is empty")
	}
}

func TestDTClientContextHeader_WithAIAgentAndContext(t *testing.T) {
	clearAIAgentEnvVars(t)
	t.Setenv("AI_AGENT", "1")

	h := dtClientContextHeader("anomaly-investigation")
	m := parseDTClientContext(t, h)

	if m["app"] != "dtctl" {
		t.Errorf("app = %q, want %q", m["app"], "dtctl")
	}
	if m["agent"] != "generic-ai" {
		t.Errorf("agent = %q, want %q", m["agent"], "generic-ai")
	}
	if m["context"] != "anomaly-investigation" {
		t.Errorf("context = %q, want %q", m["context"], "anomaly-investigation")
	}
}

func TestDQLExecutor_ClientContextHeader_OnExecuteAndPoll(t *testing.T) {
	tests := []struct {
		name          string
		clientContext string
		wantContext   string // empty means key must be absent
		setAIAgentEnv bool
		wantAgent     string
	}{
		{
			name:          "no context no agent",
			clientContext: "",
			wantContext:   "",
			wantAgent:     "",
		},
		{
			name:          "with caller context",
			clientContext: "root-cause-analysis",
			wantContext:   "root-cause-analysis",
			wantAgent:     "",
		},
		{
			name:          "with AI agent auto-detected",
			clientContext: "",
			setAIAgentEnv: true,
			wantAgent:     "generic-ai",
		},
		{
			name:          "with both caller context and AI agent",
			clientContext: "incident-response",
			setAIAgentEnv: true,
			wantContext:   "incident-response",
			wantAgent:     "generic-ai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAIAgentEnvVars(t) // prevent CLAUDECODE (or similar) from leaking in from the test runner
			if tt.setAIAgentEnv {
				t.Setenv("AI_AGENT", "1")
			}

			var executeHeader, pollHeader string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/platform/storage/query/v1/query:execute":
					executeHeader = r.Header.Get("dt-client-context")
					// Return 202 to force a poll cycle
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(DQLQueryResponse{
						State:        "RUNNING",
						RequestToken: "tok-abc",
					})
				case "/platform/storage/query/v1/query:poll":
					pollHeader = r.Header.Get("dt-client-context")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(DQLQueryResponse{
						State:  "SUCCEEDED",
						Result: &DQLResult{Records: []map[string]interface{}{{"k": "v"}}},
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			executor := NewDQLExecutor(c)
			_, err = executor.ExecuteQueryWithContext(context.Background(), "fetch logs", DQLExecuteOptions{
				ClientContext: tt.clientContext,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for label, raw := range map[string]string{"execute": executeHeader, "poll": pollHeader} {
				m := parseDTClientContext(t, raw)
				if m["app"] != "dtctl" {
					t.Errorf("[%s] app = %q, want %q", label, m["app"], "dtctl")
				}
				if m["version"] == "" {
					t.Errorf("[%s] version must not be empty", label)
				}
				if tt.wantContext != "" && m["context"] != tt.wantContext {
					t.Errorf("[%s] context = %q, want %q", label, m["context"], tt.wantContext)
				}
				if tt.wantContext == "" {
					if _, hasCtx := m["context"]; hasCtx {
						t.Errorf("[%s] context field should be absent when ClientContext is empty", label)
					}
				}
				if tt.wantAgent != "" && m["agent"] != tt.wantAgent {
					t.Errorf("[%s] agent = %q, want %q", label, m["agent"], tt.wantAgent)
				}
				if tt.wantAgent == "" {
					if _, hasAgent := m["agent"]; hasAgent {
						t.Errorf("[%s] agent field should be absent when no AI agent env var is set", label)
					}
				}
			}
		})
	}
}

func TestDQLExecutor_ClientContextHeader_OnVerify(t *testing.T) {
	clearAIAgentEnvVars(t)
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("dt-client-context")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DQLVerifyResponse{Valid: true})
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	_, err = executor.VerifyQuery("fetch logs", DQLVerifyOptions{
		ClientContext: "syntax-check",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := parseDTClientContext(t, receivedHeader)
	if m["app"] != "dtctl" {
		t.Errorf("app = %q, want %q", m["app"], "dtctl")
	}
	if m["context"] != "syntax-check" {
		t.Errorf("context = %q, want %q", m["context"], "syntax-check")
	}
}

func TestDQLExecutor_ClientContextHeader_OnPublicCancel(t *testing.T) {
	clearAIAgentEnvVars(t)
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("dt-client-context")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	executor.CancelQuery("tok-xyz")

	m := parseDTClientContext(t, receivedHeader)
	if m["app"] != "dtctl" {
		t.Errorf("app = %q, want %q", m["app"], "dtctl")
	}
	if _, hasCtx := m["context"]; hasCtx {
		t.Error("public CancelQuery should omit context field (no caller context available)")
	}
}

func TestDQLExecutor_ClientContextHeader_OnInternalCancel(t *testing.T) {
	// When a context is cancelled while poll is in-flight, the internal cancel path
	// should carry the full ClientContext from the opts.
	clearAIAgentEnvVars(t)

	var cancelHeader string
	cancelCalled := make(chan struct{}, 1)
	// unblockPoll lets the test release the poll handler after cancellation so the
	// httptest server can close without blocking.
	unblockPoll := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(DQLQueryResponse{
				State:        "RUNNING",
				RequestToken: "tok-cancel-test",
			})
		case "/platform/storage/query/v1/query:cancel":
			cancelHeader = r.Header.Get("dt-client-context")
			cancelCalled <- struct{}{}
			w.WriteHeader(http.StatusOK)
		case "/platform/storage/query/v1/query:poll":
			// Hold the poll response until the test unblocks it, simulating a long-running query.
			<-unblockPoll
			w.WriteHeader(http.StatusServiceUnavailable) // any non-success is fine; the context is cancelled
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := NewDQLExecutor(c)

	done := make(chan error, 1)
	go func() {
		_, err := executor.ExecuteQueryWithContext(ctx, "fetch logs", DQLExecuteOptions{
			ClientContext: "incident-response",
		})
		done <- err
	}()

	// Cancel once poll is in-flight, then unblock the poll handler.
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(unblockPoll)

	select {
	case <-cancelCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for query:cancel to be called")
	}
	<-done

	m := parseDTClientContext(t, cancelHeader)
	if m["app"] != "dtctl" {
		t.Errorf("app = %q, want %q", m["app"], "dtctl")
	}
	if m["context"] != "incident-response" {
		t.Errorf("context = %q, want %q", m["context"], "incident-response")
	}
}

func TestWindowAdvice(t *testing.T) {
	trap := `fetch logs | filter timestamp >= now() - 24h and timestamp < now() - 12h | summarize count()`
	cases := []struct {
		name    string
		query   string
		records []map[string]interface{}
		opts    DQLExecuteOptions
		want    bool
	}{
		{"trap query, no rows", trap, nil, DQLExecuteOptions{}, true},
		{"trap query, single zero-count row", trap, []map[string]interface{}{{"count()": "0"}}, DQLExecuteOptions{}, true},
		{"trap query, nonzero count row", trap, []map[string]interface{}{{"count()": "4136117"}}, DQLExecuteOptions{}, false},
		{"trap query, many rows", trap, []map[string]interface{}{{"a": "1"}, {"a": "2"}}, DQLExecuteOptions{}, false},
		{"explicit from: in query", `fetch logs, from:now()-24h | filter timestamp < now()-12h | summarize c = count()`, nil, DQLExecuteOptions{}, false},
		{"timeframe flag set", trap, nil, DQLExecuteOptions{DefaultTimeframeStart: "2026-01-01T00:00:00Z"}, false},
		// Since the t9 trap fix, an empty result WITHOUT a timestamp filter
		// also fires (default-window wording) as long as no explicit window
		// was set anywhere.
		{"no timestamp filter", `fetch logs | summarize count()`, nil, DQLExecuteOptions{}, true},
		{"filter on another field only", `fetch logs | filter loglevel == "ERROR"`, nil, DQLExecuteOptions{}, true},
		{"no timestamp filter, explicit window", `fetch logs, from:now()-7d | summarize count()`, nil, DQLExecuteOptions{}, false},
		{"no timestamp filter, nonempty result", `fetch logs | summarize count()`, []map[string]interface{}{{"count()": "42"}}, DQLExecuteOptions{}, false},
		{"metric.series discovery, empty", `fetch metric.series | filter contains(metric.key, "oom")`, nil, DQLExecuteOptions{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := windowAdvice(tc.query, tc.records, tc.opts)
			if (len(got) > 0) != tc.want {
				t.Errorf("windowAdvice(%q, %v) = %v, want fired=%v", tc.query, tc.records, got, tc.want)
			}
		})
	}
	// The two empty-result shapes carry distinct wording: the timestamp-filter
	// trap names the filter; the plain default-window one suggests widening;
	// metric.series gets the discovery-specific line.
	tsAdvice := windowAdvice(trap, nil, DQLExecuteOptions{})
	if len(tsAdvice) == 0 || !strings.Contains(tsAdvice[0], "filter timestamp") {
		t.Errorf("timestamp-trap advice = %v", tsAdvice)
	}
	defAdvice := windowAdvice(`fetch logs | summarize count()`, nil, DQLExecuteOptions{})
	if len(defAdvice) == 0 || !strings.Contains(defAdvice[0], "DEFAULT query window") {
		t.Errorf("default-window advice = %v", defAdvice)
	}
	msAdvice := windowAdvice(`fetch metric.series | filter contains(metric.key, "oom")`, nil, DQLExecuteOptions{})
	if len(msAdvice) == 0 || !strings.Contains(msAdvice[0], "metric.series") {
		t.Errorf("metric.series advice = %v", msAdvice)
	}
}

// TestLookbackAdvice locks the note riding SUCCESSFUL dt.entity.* fetches
// (evals: 17-host lookback census reported against 12 live hosts in five
// consecutive control batches — via queries that returned rc 0, so the
// error-side redirect never fired).
func TestLookbackAdvice(t *testing.T) {
	s := lookbackAdvice(`fetch dt.entity.host | summarize count()`)
	if len(s) != 1 || !strings.Contains(s[0], `smartscapeNodes "HOST"`) || !strings.Contains(s[0], "LOOKBACK") {
		t.Errorf("advice = %v", s)
	}
	s = lookbackAdvice(`fetch dt.entity.aws_lambda_function | limit 10`)
	if len(s) != 1 || !strings.Contains(s[0], `smartscapeNodes "AWS_LAMBDA_FUNCTION"`) {
		t.Errorf("advice = %v", s)
	}
	for _, q := range []string{
		`fetch logs | summarize count()`,
		`smartscapeNodes "HOST" | summarize count()`,
		`fetch dt.davis.events`,
	} {
		if s := lookbackAdvice(q); len(s) != 0 {
			t.Errorf("unexpected advice for %q: %v", q, s)
		}
	}
}

// TestFetchExecTimeLimitIsPartial covers a truncation Grail reports that dtctl
// used to read as a complete result.
//
// `metrics | summarize count()` on a busy tenant returns a capped row count
// alongside FETCH_EXEC_TIME_LIMIT ("the data couldn't be read within the
// internal time limit of 10008 ms"). Unclassified, that result looked
// complete, and every row below the cut became a fabricated absence — the same
// failure `inventoryMaxResultRecords` exists to prevent, arriving by a
// different route.
func TestFetchExecTimeLimitIsPartial(t *testing.T) {
	for _, n := range []QueryNotification{
		{NotificationType: "FETCH_EXEC_TIME_LIMIT", Message: "Your result is incomplete because the data couldn't be read within the internal time limit of 10008 ms. Please try narrowing your timeframe."},
		// Deployments that do not tag the notification must still be caught.
		{NotificationType: "", Message: "Your result is incomplete because the data couldn't be read within the internal time limit of 10008 ms."},
	} {
		if !ResultIsPartial(n) {
			t.Errorf("ResultIsPartial(%+v) = false, want true: a read cut short by the time limit is not a complete result", n)
		}
	}

	// Guard the boundary: an ordinary informational notification is not truncation.
	if ResultIsPartial(QueryNotification{NotificationType: "PARAMETERS_SHOULD_BE_GROUPED", Message: "The parameters should be grouped with curly braces: {}."}) {
		t.Error("an informational notification must not mark the result partial")
	}
}

// TestPrintResults_AgentJQ_KeepsEnvelope covers #413: `query --agent --jq` used
// to emit a bare {result, metadata} object, so a consumer had no `ok` field to
// test and could not tell success from failure. The envelope now survives the
// filter, and the filter still sees the result payload ({records, metadata?}).
func TestPrintResults_AgentJQ_KeepsEnvelope(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		{"display_id": "P-1"},
		{"display_id": "P-2"},
	}
	result := &DQLQueryResponse{Records: records}
	opts := DQLExecuteOptions{
		OutputFormat: "json",
		AgentMode:    true,
		JQFilter:     ".records",
	}

	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch dt.davis.problems", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}

	var resp struct {
		OK      bool `json:"ok"`
		Result  []map[string]interface{}
		Context *output.ResponseContext `json:"context"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if !resp.OK {
		t.Errorf("ok = false, want true; output: %s", out)
	}
	if len(resp.Result) != 2 || resp.Result[0]["display_id"] != "P-1" {
		t.Errorf("result = %#v, want the two filtered records", resp.Result)
	}
	if resp.Context == nil || resp.Context.Verb != "query" {
		t.Errorf("context = %#v, want verb=query", resp.Context)
	}
	if resp.Context != nil && (resp.Context.Total == nil || *resp.Context.Total != 2) {
		t.Errorf("context.total = %v, want 2", resp.Context.Total)
	}
}

// TestPrintResults_AgentJQ_WrongShapeErrors is the false-negative the issue was
// filed for: a filter written against the envelope (".result.records") must fail
// loudly rather than print a confident null.
func TestPrintResults_AgentJQ_WrongShapeErrors(t *testing.T) {
	e := &DQLExecutor{}
	result := &DQLQueryResponse{Records: []map[string]interface{}{{"display_id": "P-1"}}}
	opts := DQLExecuteOptions{
		OutputFormat: "json",
		AgentMode:    true,
		JQFilter:     ".result.records",
	}

	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch dt.davis.problems", result, opts)
	})
	if printErr == nil {
		t.Fatalf("printResults returned nil error; stdout was: %s", out)
	}

	var jqErr *output.JQError
	if !errors.As(printErr, &jqErr) {
		t.Fatalf("err = %T (%v), want *output.JQError", printErr, printErr)
	}
	if !strings.Contains(jqErr.Message, "[records]") {
		t.Errorf("message does not name the payload keys: %q", jqErr.Message)
	}
	if len(out) != 0 {
		t.Errorf("nothing should be printed on a filter error, got: %s", out)
	}
}

// TestPrintResults_AgentJQ_EmptyResultYieldsEmptyList keeps the counterpart
// honest: a query that genuinely matched no rows still filters cleanly, and
// `.records` answers [] rather than the null that would be indistinguishable
// from the shape mismatch above.
func TestPrintResults_AgentJQ_EmptyResultYieldsEmptyList(t *testing.T) {
	e := &DQLExecutor{}
	result := &DQLQueryResponse{Result: &DQLResult{Records: nil}}
	opts := DQLExecuteOptions{
		OutputFormat: "json",
		AgentMode:    true,
		JQFilter:     ".records",
	}

	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs | limit 0", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}

	var resp struct {
		OK     bool          `json:"ok"`
		Result []interface{} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if !resp.OK {
		t.Errorf("ok = false, want true; output: %s", out)
	}
	if resp.Result == nil || len(resp.Result) != 0 {
		t.Errorf("result = %#v, want an empty list", resp.Result)
	}
}

// TestPrintResults_AgentJQ_MetadataIsReachableAndPreserved pins both halves of
// how --metadata behaves under --jq: it is inside the filter's input (so
// `.metadata` reaches it) *and* kept as the envelope's metadata sibling, where
// the unfiltered envelope puts it — so a filter that narrows to `.records`
// doesn't silently drop it.
func TestPrintResults_AgentJQ_MetadataIsReachableAndPreserved(t *testing.T) {
	e := &DQLExecutor{}
	result := &DQLQueryResponse{
		Records: []map[string]interface{}{{"host": "web-01"}},
		Metadata: &DQLMetadata{Grail: &GrailMetadata{
			ExecutionTimeMilliseconds: 42,
		}},
	}
	opts := DQLExecuteOptions{
		OutputFormat:   "json",
		AgentMode:      true,
		JQFilter:       ".metadata",
		MetadataFields: []string{"executionTimeMilliseconds"},
	}

	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}

	var resp struct {
		Result   map[string]interface{} `json:"result"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	// The filter ran on the payload's metadata.
	if resp.Result["executionTimeMilliseconds"] != float64(42) {
		t.Errorf("result = %#v, want the filtered metadata object", resp.Result)
	}
	// And the envelope still carries it as a sibling of result.
	if resp.Metadata["executionTimeMilliseconds"] != float64(42) {
		t.Errorf("envelope metadata = %#v, want it preserved", resp.Metadata)
	}
}

// TestPrintResults_JQ_EmptyResultIsNotAShapeMismatch guards the plain (non-agent)
// --jq path: a query that matched no rows must still render `records` as an empty
// array, or the filter would see null and be rejected as a shape mismatch (#413).
func TestPrintResults_JQ_EmptyResultIsNotAShapeMismatch(t *testing.T) {
	e := &DQLExecutor{}
	result := &DQLQueryResponse{Result: &DQLResult{Records: nil}}
	opts := DQLExecuteOptions{OutputFormat: "json", JQFilter: ".records"}

	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs | limit 0", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}

	var got []interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("out = %#v, want an empty list", got)
	}
}

func TestDQLExecutor_WithStreamsKeepsResultsOffTheProcessStreams(t *testing.T) {
	records := []map[string]interface{}{{"host": "a"}, {"host": "b"}}
	var out, errOut bytes.Buffer
	e := (&DQLExecutor{}).WithStreams(&out, &errOut)

	onProcessStdout := captureStdout(t, func() {
		if err := e.printResults("fetch logs", &DQLQueryResponse{Records: records},
			DQLExecuteOptions{OutputFormat: "jsonl"}); err != nil {
			t.Errorf("printResults: %v", err)
		}
	})

	if len(onProcessStdout) != 0 {
		t.Errorf("results reached the process stdout: %q", onProcessStdout)
	}
	if got := strings.Count(out.String(), "\n"); got != len(records) {
		t.Errorf("got %d lines on the executor's writer, want %d: %q", got, len(records), out.String())
	}
}

func TestDQLExecutor_WithVFSReadsQueryFilesFromItsEnv(t *testing.T) {
	// The file exists on the host but not in the executor's filesystem: reading
	// it must fail, because the executor resolves paths through its own Env.
	hostPath := filepath.Join(t.TempDir(), "q.dql")
	if err := os.WriteFile(hostPath, []byte("fetch logs"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := vfs.Env{FS: vfs.NewMapFS(nil)}

	err := (&DQLExecutor{}).WithVFS(env).ExecuteFromFile(hostPath, "json")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ExecuteFromFile error = %v, want it to wrap fs.ErrNotExist from the executor's own filesystem", err)
	}
}
