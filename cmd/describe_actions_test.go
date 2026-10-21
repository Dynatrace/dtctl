package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
)

func TestUseActionDescribeTextView(t *testing.T) {
	origAgent := agentMode(context.Background())
	origFormat := outputFormat(context.Background())
	t.Cleanup(func() {
		gFlags.agentMode = origAgent
		gFlags.outputFormat = origFormat
	})

	cases := []struct {
		name   string
		agent  bool
		format string
		want   bool
	}{
		{"default table, non-agent", false, "table", true},
		{"empty format, non-agent", false, "", true},
		{"json, non-agent", false, "json", false},
		{"yaml, non-agent", false, "yaml", false},
		// Agent mode must never take the human path, even with the default
		// "table" format that agent mode leaves in place.
		{"table, agent mode", true, "table", false},
		{"empty format, agent mode", true, "", false},
		{"json, agent mode", true, "json", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gFlags.agentMode = tc.agent
			gFlags.outputFormat = tc.format
			if got := useActionDescribeTextView(context.Background()); got != tc.want {
				t.Errorf("useActionDescribeTextView() = %v, want %v (agent=%v format=%q)",
					got, tc.want, tc.agent, tc.format)
			}
		})
	}
}

func TestDescribeActionTable(t *testing.T) {
	output.SetPlainMode(true)
	output.ResetColorCache()
	t.Cleanup(output.ResetColorCache)

	t.Run("every field, with the manifest attributes inline", func(t *testing.T) {
		action := &appengine.AppAction{
			AppID:       "example.connector",
			ActionName:  "create-thing",
			Title:       "Create thing",
			Description: "Creates a thing",
			Stateful:    true,
			FullName:    "example.connector:create-thing",
			Extra: map[string]json.RawMessage{
				"widget": json.RawMessage(`"/actions/create-thing"`),
				"docs":   json.RawMessage(`{"inputSchema":{"type":"object"}}`),
			},
		}

		var buf bytes.Buffer
		if err := describeActionTable(&buf, action); err != nil {
			t.Fatalf("describeActionTable: %v", err)
		}
		got := buf.String()

		for _, want := range []string{
			"Action:       create-thing",
			"Full Name:    example.connector:create-thing",
			"App:          example.connector",
			"Title:        Create thing",
			"Description:  Creates a thing",
			"Stateful:     yes",
			"\ndocs:\n  inputSchema:\n    type: object\n",
			"\nwidget: /actions/create-thing\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "Additional manifest keys") {
			t.Errorf("attributes are wrapped in a section of dtctl's own:\n%s", got)
		}
	})

	t.Run("optional fields and an empty Extra are omitted", func(t *testing.T) {
		action := &appengine.AppAction{
			AppID:      "example.app",
			ActionName: "list-things",
			FullName:   "example.app:list-things",
		}

		var buf bytes.Buffer
		if err := describeActionTable(&buf, action); err != nil {
			t.Fatalf("describeActionTable: %v", err)
		}
		got := buf.String()

		// Stateful prints only when set: the manifest omits it rather than
		// declaring it false, so "Stateful: no" would assert something the
		// manifest never said.
		for _, absent := range []string{"Title:", "Description:", "Stateful:"} {
			if strings.Contains(got, absent) {
				t.Errorf("output contains %q although the field is empty:\n%s", absent, got)
			}
		}
		if !strings.Contains(got, "Action:       list-things") {
			t.Errorf("output missing the action name:\n%s", got)
		}
	})
}
