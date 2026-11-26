package apply

import (
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/resources/extension"
	"github.com/Dynatrace/dtctl/pkg/safety"
)

// dryRunExtensionConfig performs dry-run validation for extension monitoring configs
func (a *Applier) dryRunExtensionConfig(doc map[string]interface{}) (ApplyResult, error) {
	extensionName, _ := doc["extensionName"].(string)
	objectID, _ := doc["objectId"].(string)
	scope, _ := doc["scope"].(string)

	// Validate required fields to align dry-run with real apply behavior
	if extensionName == "" {
		return nil, fmt.Errorf("extensionName is required for extension monitoring configuration")
	}

	action := ActionCreated
	if objectID != "" {
		action = ActionUpdated
	}

	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: "extension_config",
			ID:           objectID,
		},
		ExtensionName: extensionName,
		Scope:         scope,
	}, nil
}

// applyExtensionConfig applies an extension monitoring configuration.
// Detects create vs update by checking for an objectId field in the payload.
func (a *Applier) applyExtensionConfig(data []byte) (ApplyResult, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse extension config JSON: %w", err)
	}

	extensionName, _ := raw["extensionName"].(string)
	if extensionName == "" {
		return nil, fmt.Errorf("extensionName is required in extension config payload")
	}

	objectID, _ := raw["objectId"].(string)

	// Build the create/update body (scope + value only)
	var config extension.MonitoringConfigurationCreate
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse extension config body: %w", err)
	}

	handler := extension.NewHandler(a.client)

	if objectID == "" {
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.CreateMonitoringConfiguration(extensionName, config)
		if err != nil {
			return nil, fmt.Errorf("failed to create extension monitoring configuration: %w", err)
		}

		return &ExtensionConfigApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "extension_config",
				ID:           result.ObjectID,
			},
			ExtensionName: extensionName,
			Scope:         result.Scope,
		}, nil
	}

	if err := a.checkSafety(safety.OperationUpdate, safety.OwnershipUnknown); err != nil {
		return nil, err
	}

	result, err := handler.UpdateMonitoringConfiguration(extensionName, objectID, config)
	if err != nil {
		return nil, fmt.Errorf("failed to update extension monitoring configuration: %w", err)
	}

	return &ExtensionConfigApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "extension_config",
			ID:           result.ObjectID,
		},
		ExtensionName: extensionName,
		Scope:         result.Scope,
	}, nil
}
