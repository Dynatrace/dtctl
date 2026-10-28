//go:build integration
// +build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/anomalydetector"
	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/azureconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/bucket"
	"github.com/Dynatrace/dtctl/pkg/resources/document"
	"github.com/Dynatrace/dtctl/pkg/resources/edgeconnect"
	"github.com/Dynatrace/dtctl/pkg/resources/extension"
	"github.com/Dynatrace/dtctl/pkg/resources/gcpconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/lookup"
	"github.com/Dynatrace/dtctl/pkg/resources/schedulingrule"
	"github.com/Dynatrace/dtctl/pkg/resources/segment"
	"github.com/Dynatrace/dtctl/pkg/resources/settings"
	"github.com/Dynatrace/dtctl/pkg/resources/slo"
	"github.com/Dynatrace/dtctl/pkg/resources/workflow"
)

// Resource represents a tracked resource for cleanup
type Resource struct {
	Type          string // "workflow", "dashboard", "notebook", "bucket", "extension-config"
	ID            string // Resource ID or name
	Name          string // Human-readable name for logging
	Version       int    // Version for documents (dashboards/notebooks)
	ExtensionName string // Extension name (for extension-config resources)
}

// CleanupTracker tracks created resources and handles cleanup
type CleanupTracker struct {
	client    *client.Client
	resources []Resource
}

// NewCleanupTracker creates a new cleanup tracker
func NewCleanupTracker(c *client.Client) *CleanupTracker {
	return &CleanupTracker{
		client:    c,
		resources: make([]Resource, 0),
	}
}

// Track adds a resource to be cleaned up
func (c *CleanupTracker) Track(resourceType, id, name string) {
	c.resources = append(c.resources, Resource{
		Type: resourceType,
		ID:   id,
		Name: name,
	})
}

// TrackDocument adds a document resource (dashboard/notebook) with version for cleanup
func (c *CleanupTracker) TrackDocument(resourceType, id, name string, version int) {
	c.resources = append(c.resources, Resource{
		Type:    resourceType,
		ID:      id,
		Name:    name,
		Version: version,
	})
}

// TrackExtensionConfig adds an extension monitoring configuration for cleanup
func (c *CleanupTracker) TrackExtensionConfig(extensionName, configID string) {
	c.resources = append(c.resources, Resource{
		Type:          "extension-config",
		ID:            configID,
		Name:          fmt.Sprintf("%s/%s", extensionName, configID),
		ExtensionName: extensionName,
	})
}

// TrackCloudMonitoringConfig adds a hyperscaler monitoring configuration for
// cleanup. cloud is "aws", "azure" or "gcp" — the resource types are distinct
// because each cloud has its own extension and its own handler.
func (c *CleanupTracker) TrackCloudMonitoringConfig(cloud, configID, name string) {
	c.resources = append(c.resources, Resource{
		Type: cloud + "-monitoring-config",
		ID:   configID,
		Name: name,
	})
}

// TrackCloudConnection adds a hyperscaler connection (credential) for cleanup.
func (c *CleanupTracker) TrackCloudConnection(cloud, objectID, name string) {
	c.resources = append(c.resources, Resource{
		Type: cloud + "-connection",
		ID:   objectID,
		Name: name,
	})
}

// Untrack removes a resource from cleanup tracking (when manually deleted in test)
func (c *CleanupTracker) Untrack(resourceType, id string) {
	for i, r := range c.resources {
		if r.Type == resourceType && r.ID == id {
			// Remove from slice
			c.resources = append(c.resources[:i], c.resources[i+1:]...)
			return
		}
	}
}

// Cleanup deletes all tracked resources in reverse order
// Returns error if any cleanup operation fails
func (c *CleanupTracker) Cleanup(t *testing.T) error {
	t.Helper()

	// Delete in reverse order (LIFO - last created, first deleted)
	for i := len(c.resources) - 1; i >= 0; i-- {
		resource := c.resources[i]

		t.Logf("Cleaning up %s: %s (ID: %s)", resource.Type, resource.Name, resource.ID)

		if err := c.deleteResource(resource); err != nil {
			// Log the error but continue cleanup
			t.Errorf("Failed to delete %s %s: %v", resource.Type, resource.ID, err)
			continue
		}

		// Verify deletion (expect 404)
		if err := c.verifyDeletion(resource); err != nil {
			t.Errorf("Cleanup verification failed for %s %s: %v", resource.Type, resource.ID, err)
		} else {
			t.Logf("Successfully cleaned up and verified deletion of %s: %s", resource.Type, resource.Name)
		}
	}

	return nil
}

// deleteResource deletes a single resource based on its type
func (c *CleanupTracker) deleteResource(resource Resource) error {
	switch resource.Type {
	case "workflow":
		handler := workflow.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "scheduling-rule":
		handler := schedulingrule.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "dashboard", "notebook":
		handler := document.NewHandler(c.client)
		// For documents, we need the version for optimistic locking
		// If version is 0, we need to fetch it first
		version := resource.Version
		if version == 0 {
			doc, err := handler.Get(resource.ID)
			if err != nil {
				// Ignore 404 errors - document already deleted or doesn't exist
				if isNotFoundError(err) {
					return nil
				}
				return fmt.Errorf("failed to get document version: %w", err)
			}
			version = doc.Version
		}
		err := handler.Delete(resource.ID, version)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "bucket":
		handler := bucket.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors and "in use" errors during cleanup
		// Buckets may be in "creating" state or have dependencies
		if err != nil && (isNotFoundError(err) || isInUseError(err)) {
			return nil
		}
		return err

	case "settings":
		handler := settings.NewHandler(c.client)
		// Delete handles optimistic locking internally
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "slo":
		handler := slo.NewHandler(c.client)
		// For SLO, we need the version for optimistic locking
		// Get the current version first
		sloObj, err := handler.Get(resource.ID)
		if err != nil {
			// Ignore 404 errors - SLO already deleted
			if isNotFoundError(err) {
				return nil
			}
			return fmt.Errorf("failed to get SLO version: %w", err)
		}
		err = handler.Delete(resource.ID, sloObj.Version)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "edgeconnect":
		handler := edgeconnect.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "lookup":
		handler := lookup.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "segment":
		handler := segment.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "extension-config":
		handler := extension.NewHandler(c.client)
		err := handler.DeleteMonitoringConfiguration(resource.ExtensionName, resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "anomalydetector":
		handler := anomalydetector.NewHandler(c.client)
		err := handler.Delete(resource.ID)
		// Ignore 404 errors - resource already deleted is OK
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "aws-monitoring-config":
		err := awsmonitoringconfig.NewHandler(c.client).Delete(resource.ID)
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "azure-monitoring-config":
		err := azuremonitoringconfig.NewHandler(c.client).Delete(resource.ID)
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "gcp-monitoring-config":
		err := gcpmonitoringconfig.NewHandler(c.client).Delete(resource.ID)
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "aws-connection":
		err := awsconnection.NewHandler(c.client).Delete(resource.ID)
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "azure-connection":
		err := azureconnection.NewHandler(c.client).Delete(resource.ID)
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	case "gcp-connection":
		err := gcpconnection.NewHandler(c.client).Delete(resource.ID)
		if err != nil && isNotFoundError(err) {
			return nil
		}
		return err

	default:
		return fmt.Errorf("unknown resource type: %s", resource.Type)
	}
}

// isNotFoundError checks if an error is a 404 not found error
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "Not found")
}

// isInUseError checks if an error is an "in use" error (for buckets)
func isInUseError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "still in use")
}

// verifyDeletion verifies that a resource was actually deleted
// Expects a 404 response when trying to GET the resource
func (c *CleanupTracker) verifyDeletion(resource Resource) error {
	switch resource.Type {
	case "workflow":
		handler := workflow.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("workflow %s still exists after deletion", resource.ID)

	case "scheduling-rule":
		handler := schedulingrule.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("scheduling rule %s still exists after deletion", resource.ID)

	case "dashboard", "notebook":
		handler := document.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("document %s still exists after deletion", resource.ID)

	case "bucket":
		handler := bucket.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("bucket %s still exists after deletion", resource.ID)

	case "settings":
		handler := settings.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("settings %s still exists after deletion", resource.ID)

	case "slo":
		handler := slo.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("slo %s still exists after deletion", resource.ID)

	case "edgeconnect":
		handler := edgeconnect.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("edgeconnect %s still exists after deletion", resource.ID)

	case "lookup":
		handler := lookup.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("lookup %s still exists after deletion", resource.ID)

	case "segment":
		handler := segment.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("segment %s still exists after deletion", resource.ID)

	case "extension-config":
		handler := extension.NewHandler(c.client)
		_, err := handler.GetMonitoringConfiguration(resource.ExtensionName, resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("extension-config %s still exists after deletion", resource.ID)

	case "anomalydetector":
		handler := anomalydetector.NewHandler(c.client)
		_, err := handler.Get(resource.ID)
		if err != nil {
			// We expect an error (404) - this is success
			return nil
		}
		return fmt.Errorf("anomaly detector %s still exists after deletion", resource.ID)

	case "aws-monitoring-config":
		if _, err := awsmonitoringconfig.NewHandler(c.client).Get(resource.ID); err != nil {
			return nil
		}
		return fmt.Errorf("aws monitoring config %s still exists after deletion", resource.ID)

	case "azure-monitoring-config":
		if _, err := azuremonitoringconfig.NewHandler(c.client).Get(resource.ID); err != nil {
			return nil
		}
		return fmt.Errorf("azure monitoring config %s still exists after deletion", resource.ID)

	case "gcp-monitoring-config":
		if _, err := gcpmonitoringconfig.NewHandler(c.client).Get(resource.ID); err != nil {
			return nil
		}
		return fmt.Errorf("gcp monitoring config %s still exists after deletion", resource.ID)

	case "aws-connection":
		if _, err := awsconnection.NewHandler(c.client).Get(resource.ID); err != nil {
			return nil
		}
		return fmt.Errorf("aws connection %s still exists after deletion", resource.ID)

	case "azure-connection":
		if _, err := azureconnection.NewHandler(c.client).Get(resource.ID); err != nil {
			return nil
		}
		return fmt.Errorf("azure connection %s still exists after deletion", resource.ID)

	case "gcp-connection":
		if _, err := gcpconnection.NewHandler(c.client).Get(resource.ID); err != nil {
			return nil
		}
		return fmt.Errorf("gcp connection %s still exists after deletion", resource.ID)

	default:
		return fmt.Errorf("unknown resource type: %s", resource.Type)
	}
}
