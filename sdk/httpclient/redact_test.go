package httpclient

import (
	"strings"
	"testing"
)

func TestRedactBodyForDebug(t *testing.T) {
	in := `{"name":"ca","certificate":"c2VjcmV0","password":"","nested":{"Token":"tok-123"},"list":[{"clientSecret":"s"}]}`
	got := RedactBodyForDebug(in)
	for _, secret := range []string{"c2VjcmV0", "tok-123", `"s"`} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %s leaked: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"name":"ca"`) || !strings.Contains(got, `"password":""`) {
		t.Errorf("non-secret fields must survive: %s", got)
	}
	for _, unchanged := range []string{"", "not json", `{"name":"x"}`, `{broken`} {
		if RedactBodyForDebug(unchanged) != unchanged {
			t.Errorf("RedactBodyForDebug(%q) changed a body without secrets", unchanged)
		}
	}
}
