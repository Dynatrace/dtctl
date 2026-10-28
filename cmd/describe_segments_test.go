package cmd

import (
	"bytes"
	"testing"

	cmdtestutil "github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/resources/segment"
)

func TestDescribeSegmentTableGolden(t *testing.T) {
	seg := &segment.FilterSegment{
		UID:         "a1b2c3d4-e5f6-4a7b-8c9d-seg000000001",
		Name:        "Kubernetes Alpha",
		Description: "Filters data for Kubernetes cluster alpha",
		IsPublic:    true,
		Owner:       "admin@example.invalid",
		Version:     3,
		Includes: []segment.Include{
			{DataObject: "_all_data_object", Filter: `k8s.cluster.name = "alpha"`},
			{DataObject: "logs", Filter: `dt.system.bucket = "custom-logs"`},
		},
		Variables: &segment.Variables{
			Type:  "query",
			Value: `fetch logs | limit 1`,
		},
		AllowedOperations: []string{"READ", "WRITE", "DELETE"},
	}

	var buf bytes.Buffer
	printSegmentDescribeTable(&buf, seg)

	cmdtestutil.AssertGoldenStripped(t, "describe-segment/table", buf.String())
}

func TestDescribeSegmentTableGolden_Minimal(t *testing.T) {
	seg := &segment.FilterSegment{
		UID:      "b2c3d4e5-f6a7-4b8c-9d0e-seg000000002",
		Name:     "Simple Segment",
		IsPublic: false,
		Version:  1,
	}

	var buf bytes.Buffer
	printSegmentDescribeTable(&buf, seg)

	cmdtestutil.AssertGoldenStripped(t, "describe-segment/table-minimal", buf.String())
}
