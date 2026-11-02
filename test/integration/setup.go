//go:build integration
// +build integration

package integration

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/config"
)

// IntegrationEnv holds the integration test environment
type IntegrationEnv struct {
	Client     *client.Client
	Config     *config.Config
	Cleanup    *CleanupTracker
	TestPrefix string
}

// SetupIntegration sets up the integration test environment
// Skips the test if required environment variables are not set
func SetupIntegration(t *testing.T) *IntegrationEnv {
	t.Helper()

	// Check for required environment variables
	envURL := os.Getenv("DTCTL_INTEGRATION_ENV")
	if envURL == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_ENV not set")
	}

	token := os.Getenv("DTCTL_INTEGRATION_TOKEN")
	if token == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_TOKEN not set")
	}

	// Create client
	c, err := client.New(envURL, token)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Create minimal config (for reference, not persisted)
	cfg := &config.Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "integration-test",
		Contexts: []config.NamedContext{
			{
				Name: "integration-test",
				Context: config.Context{
					Environment: envURL,
					TokenRef:    "integration-token",
				},
			},
		},
		Tokens: []config.NamedToken{
			{
				Name:  "integration-token",
				Token: token,
			},
		},
	}

	// Generate unique test prefix
	testPrefix := generateTestPrefix()

	// Initialize cleanup tracker
	cleanup := NewCleanupTracker(c)

	t.Logf("Integration test environment initialized with prefix: %s", testPrefix)

	return &IntegrationEnv{
		Client:     c,
		Config:     cfg,
		Cleanup:    cleanup,
		TestPrefix: testPrefix,
	}
}

// generateTestPrefix creates a unique prefix for test resources
// Format: dtctl-test-{timestamp}-{random}
func generateTestPrefix() string {
	timestamp := time.Now().Unix()
	random := randomString(6)
	return fmt.Sprintf("dtctl-test-%d-%s", timestamp, random)
}

// randomString generates a random hex string of the specified length
func randomString(length int) string {
	bytes := make([]byte, length/2+1)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based randomness
		return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	}
	return hex.EncodeToString(bytes)[:length]
}
