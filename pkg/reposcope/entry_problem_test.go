package reposcope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Problem is the check a file's entries go through, so what it refuses a
// file refuses with the same words, and what it accepts a file holds.
func TestEntryProblemIsTheFileCheck(t *testing.T) {
	for _, e := range []Entry{
		{Name: "checkout", Workloads: []Workload{{Namespace: "payments", Name: "checkout"}}},
		{Name: "checkout", Workloads: []Workload{{Namespace: "Payments", Name: "checkout"}}},
		{Name: "checkout", ServiceNames: []string{`my"svc`}},
		{Name: "checkout", Services: []string{"dt.smartscape.service-1234"}},
		{Name: "checkout", ProcessGroups: []string{"PROCESS_GROUP-0000000000000001"}},
		{Name: "Checkout", ServiceNames: []string{"checkout"}},
		{Name: "checkout"},
	} {
		f := &File{Environments: map[string][]Entry{"abc12345.apps.example.invalid": {e}}}
		err := f.check()
		if e.Problem() == "" {
			assert.Nil(t, err, "%+v", e)
			continue
		}
		require.NotNil(t, err, "%+v", e)
		assert.Contains(t, err.Msg, e.Problem())
	}
}
