package commands

import "testing"

func TestExampleAvailable(t *testing.T) {
	verbs := map[string]*MinimalVerb{
		"query": {},
		"get":   {Resources: []string{"slos"}},
	}
	cases := map[string]bool{
		`dtctl query 'fetch logs'`:       true,
		`dtctl get slos  # definitions`:  true,
		`dtctl get workflow-executions`:  false, // resource hidden
		`dtctl describe <resource> <id>`: false, // verb hidden
	}
	for ex, want := range cases {
		if got := exampleAvailable(ex, verbs); got != want {
			t.Errorf("exampleAvailable(%q) = %v, want %v", ex, got, want)
		}
	}
}
