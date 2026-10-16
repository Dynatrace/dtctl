package exec

import (
	"bytes"
	"strings"
	"testing"
)

// An executor given its own streams must keep everything it says there: rows on
// its stdout, and advisories, warnings and metadata on its stderr. A host that
// runs several invocations in one process has nowhere else to put them, and the
// process streams belong to nobody.
func TestDQLExecutor_WithStreamsKeepsAdvisoriesOffTheProcessStreams(t *testing.T) {
	tests := []struct {
		name       string
		opts       DQLExecuteOptions
		wantStderr []string
	}{
		{
			name:       "an approximation warning",
			opts:       DQLExecuteOptions{OutputFormat: "table"},
			wantStderr: []string{approximationPrefix + approxText},
		},
		{
			name:       "--include-types on a format that cannot carry types",
			opts:       DQLExecuteOptions{OutputFormat: "csv", IncludeTypes: true, EmitTypes: true, TypesRequested: true},
			wantStderr: []string{includeTypesInert},
		},
		{
			name:       "the metadata footer",
			opts:       DQLExecuteOptions{OutputFormat: "table", MetadataFields: []string{"all"}},
			wantStderr: []string{"Scanned"},
		},
		{
			name:       "metadata as csv comments",
			opts:       DQLExecuteOptions{OutputFormat: "csv", MetadataFields: []string{"all"}},
			wantStderr: []string{"# "},
		},
		{
			name:       "metadata asked of a chart",
			opts:       DQLExecuteOptions{OutputFormat: "chart", MetadataFields: []string{"all"}},
			wantStderr: []string{"--metadata is not supported with chart output formats"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			e := (&DQLExecutor{}).WithStreams(&out, &errOut)

			var leakedOut []byte
			leakedErr := captureStderr(t, func() {
				leakedOut = captureStdout(t, func() {
					if err := e.printResults("fetch logs", approximateResponse(), tt.opts); err != nil {
						t.Errorf("printResults: %v", err)
					}
				})
			})

			if len(leakedOut) != 0 {
				t.Errorf("rows reached the process stdout: %q", leakedOut)
			}
			if len(leakedErr) != 0 {
				t.Errorf("advisories reached the process stderr: %q", leakedErr)
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(errOut.String(), want) {
					t.Errorf("the executor's stderr %q lacks %q", errOut.String(), want)
				}
			}
		})
	}
}
