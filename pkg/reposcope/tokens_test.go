package reposcope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVariants(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"checkout", []string{"checkout"}},
		{"paymentService", []string{"payment-service", "payment_service", "payment.service", "paymentservice"}},
		{"PaymentService", []string{"payment-service", "payment_service", "payment.service", "paymentservice"}},
		{"payment_service", []string{"payment-service", "payment_service", "payment.service", "paymentservice"}},
		{"HTTPServer", []string{"http-server", "http_server", "http.server", "httpserver"}},
		{"checkoutV2", []string{"checkout-v2", "checkout_v2", "checkout.v2", "checkoutv2"}},
		{"registry.example.invalid/acme/checkout:1.4.2", []string{"checkout"}},
		{"registry.example.invalid/acme/checkout@sha256:00ff", []string{"checkout"}},
		{"example.invalid/acme/orders", []string{"orders"}},
		{"api", nil},
		{"Service", nil},
		{"k8s", nil},
		{"ab", nil},
		{"  ", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			assert.Equal(t, tt.want, variants(tt.raw))
		})
	}
}

func TestToken_TestName(t *testing.T) {
	tests := []struct {
		raw  string
		test bool
	}{
		{"test-platform-service", true},
		{"TestPlatform", true},
		{"tests_runner", true},
		{"mock-ledger", true},
		{"mockLedger", true},
		{"fixture-data", true},
		{"registry.example.invalid/acme/test-runner:1.0", true},
		{"load-test-runner", false},
		{"testkube", false},
		{"contest-entry", false},
		{"dev-portal", false},
		{"example-gateway", false},
		{"platform-service", false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			tk, ok := newToken(kindWorkload, tt.raw, "deploy.yaml", 1)
			require.True(t, ok)
			assert.Equal(t, tt.test, tk.testName())
		})
	}
}
