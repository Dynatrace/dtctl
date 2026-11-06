package document

import "testing"

func TestUIURL(t *testing.T) {
	const base = "https://env.example.invalid"
	tests := []struct {
		name    string
		docType string
		id      string
		want    string
	}{
		{"dashboard", "dashboard", "dash-1", base + "/ui/apps/dynatrace.dashboards/dashboard/dash-1"},
		{"notebook", "notebook", "nb-1", base + "/ui/apps/dynatrace.notebooks/notebook/nb-1"},
		// Launchpads are served by the launcher app; "dynatrace.launchpads" 404s.
		{"launchpad", "launchpad", "team-launchpad", base + "/ui/apps/dynatrace.launcher/launchpad/team-launchpad"},
		{"id is escaped as one path segment", "launchpad", "team/a?b#c:d e", base + "/ui/apps/dynatrace.launcher/launchpad/team%2Fa%3Fb%23c%3Ad%20e"},
		{"custom type has no known viewer", "acme:config", "cfg-1", ""},
		{"no id", "dashboard", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UIURL(base, tt.docType, tt.id); got != tt.want {
				t.Errorf("UIURL(%q, %q) = %q, want %q", tt.docType, tt.id, got, tt.want)
			}
		})
	}
}

func TestUIURL_TrailingSlashOnBase(t *testing.T) {
	const want = "https://env.example.invalid/ui/apps/dynatrace.notebooks/notebook/nb-1"
	for _, base := range []string{"https://env.example.invalid/", "https://env.example.invalid//"} {
		if got := UIURL(base, "notebook", "nb-1"); got != want {
			t.Errorf("UIURL(%q) = %q, want %q", base, got, want)
		}
	}
}

// TestShareURL: the link share --environment link prints is one that claim
// environment-share reads back to the same share ID and host.
func TestShareURL(t *testing.T) {
	link := ShareURL("https://abc12345.apps.dynatrace.com", "018f1234-abcd-7000-8000-000000000000")
	if want := "https://abc12345.apps.dynatrace.com/ui/document/v0/#share=018f1234-abcd-7000-8000-000000000000"; link != want {
		t.Fatalf("ShareURL = %q, want %q", link, want)
	}
	id, host, err := ParseShareRef(link)
	if err != nil || id != "018f1234-abcd-7000-8000-000000000000" || host != "abc12345.apps.dynatrace.com" {
		t.Errorf("ParseShareRef(%q) = %q, %q, %v", link, id, host, err)
	}
	if got := ShareURL("https://abc12345.apps.dynatrace.com/", "s-1"); got != "https://abc12345.apps.dynatrace.com/ui/document/v0/#share=s-1" {
		t.Errorf("ShareURL with a trailing slash on the base = %q", got)
	}
}
