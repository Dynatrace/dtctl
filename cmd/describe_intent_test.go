package cmd

import (
	"os"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
)

// TestDescribeIntentProperty pins the table view of one property (#517): an
// undeclared type is said to be undeclared rather than shown as "string",
// and accepted alias keys are listed with their patterns.
func TestDescribeIntentProperty(t *testing.T) {
	tests := []struct {
		name     string
		propName string
		prop     appengine.IntentProperty
		want     string
	}{
		{
			name:     "singular schema with pattern",
			propName: "trace_id",
			prop: appengine.IntentProperty{
				Type:         "string",
				Required:     true,
				Format:       "uuid",
				AcceptedKeys: []string{"trace_id"},
				Schema:       map[string]interface{}{"type": "string", "format": "uuid", "pattern": "^[0-9a-f]+$"},
				Description:  "Trace to open",
			},
			want: "  - trace_id: string (required)\n" +
				"    Format: uuid\n" +
				"    Pattern: ^[0-9a-f]+$\n" +
				"    Description: Trace to open\n",
		},
		{
			name:     "undeclared type",
			propName: "note",
			prop:     appengine.IntentProperty{AcceptedKeys: []string{"note"}},
			want:     "  - note: (type not declared)\n",
		},
		{
			name:     "plural schemas lists accepted keys",
			propName: "thing",
			prop: appengine.IntentProperty{
				Type:         "string",
				Required:     true,
				AcceptedKeys: []string{"dt.entity", "id"},
				Schemas: map[string]interface{}{
					"dt.entity": map[string]interface{}{"type": "string", "pattern": "^THING-"},
					"id":        map[string]interface{}{"type": "string"},
				},
			},
			want: "  - thing: string (required)\n" +
				"    Accepted keys:\n" +
				"      - dt.entity  (pattern: ^THING-)\n" +
				"      - id\n",
		},
		{
			name:     "empty schemas",
			propName: "unused",
			prop:     appengine.IntentProperty{Required: true, AcceptedKeys: []string{}, Schemas: map[string]interface{}{}},
			want: "  - unused: (type not declared) (required)\n" +
				"    Accepted keys: none (empty schemas; no payload can satisfy it)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureStdout(t, func() { describeIntentProperty(os.Stdout, tt.propName, tt.prop) })
			if got != tt.want {
				t.Errorf("output mismatch\n got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
