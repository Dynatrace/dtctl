package apply

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
)

// gcpConnectionItem holds the envelope fields a GCP connection payload carries
// around its value. Parsing lives in one place so that apply and dry run
// resolve the same object from the same document (issue #509).
type gcpConnectionItem struct {
	objectID string
	schemaID string
	scope    string
	value    gcpconnection.Value
}

// parseGCPConnectionItem reads one GCP connection payload. Both the API
// spelling ("objectId", "schemaId") and the YAML round-trip spelling
// ("objectid", "schemaid") are accepted; an absent scope defaults to
// "environment" and an absent type to serviceAccountImpersonation, the
// defaults the apply path assumes.
func parseGCPConnectionItem(item map[string]interface{}) (gcpConnectionItem, error) {
	parsed := gcpConnectionItem{
		objectID: objectIDFromDoc(item),
	}

	parsed.schemaID, _ = item["schemaId"].(string)
	if parsed.schemaID == "" {
		parsed.schemaID, _ = item["schemaid"].(string)
	}

	parsed.scope, _ = item["scope"].(string)
	if parsed.scope == "" {
		parsed.scope = "environment"
	}

	valueMap, ok := item["value"].(map[string]interface{})
	if !ok {
		return parsed, fmt.Errorf("GCP connection missing 'value' field")
	}

	valueJSON, err := json.Marshal(valueMap)
	if err != nil {
		return parsed, fmt.Errorf("failed to marshal value: %w", err)
	}
	if err := json.Unmarshal(valueJSON, &parsed.value); err != nil {
		return parsed, fmt.Errorf("failed to unmarshal value: %w", err)
	}
	if parsed.value.Type == "" {
		parsed.value.Type = gcpconnection.TypeServiceAccountImpersonation
	}

	return parsed, nil
}

// applyGCPConnection applies GCP connection configuration
func (a *Applier) applyGCPConnection(data []byte) ([]ApplyResult, error) {
	var items []map[string]interface{}

	if err := json.Unmarshal(data, &items); err != nil {
		var item map[string]interface{}
		if errSingle := json.Unmarshal(data, &item); errSingle != nil {
			return nil, fmt.Errorf("failed to parse GCP connection JSON: %w", errSingle)
		}
		items = []map[string]interface{}{item}
	}

	handler := gcpconnection.NewHandler(a.client)

	var results []ApplyResult
	var resultWarnings []string
	for _, item := range items {
		parsed, err := parseGCPConnectionItem(item)
		if err != nil {
			return nil, err
		}
		objectID, schemaID, scope, value := parsed.objectID, parsed.schemaID, parsed.scope, parsed.value

		if objectID == "" {
			existing, err := handler.FindByNameAndType(value.Name, value.Type)
			if err != nil {
				return nil, nameLookupError("GCP connection", value.Name, err)
			}
			if existing != nil {
				objectID = existing.ObjectID
			}
		}

		if objectID == "" {
			res, err := handler.Create(gcpconnection.GCPConnectionCreate{
				SchemaID: schemaID,
				Scope:    scope,
				Value:    value,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create GCP connection: %w", err)
			}

			results = append(results, &ConnectionApplyResult{
				ApplyResultBase: ApplyResultBase{
					Action:       ActionCreated,
					ResourceType: "gcp_connection",
					ID:           res.ObjectID,
					Name:         value.Name,
				},
				SchemaID: schemaID,
				Scope:    scope,
			})
		} else {
			_, err := handler.Update(objectID, value)
			if err != nil {
				return nil, fmt.Errorf("failed to update GCP connection %s: %w", objectID, err)
			}

			results = append(results, &ConnectionApplyResult{
				ApplyResultBase: ApplyResultBase{
					Action:       ActionUpdated,
					ResourceType: "gcp_connection",
					ID:           objectID,
					Name:         value.Name,
				},
				SchemaID: schemaID,
				Scope:    scope,
			})
		}
	}

	// Attach collected warnings to the last result
	if len(resultWarnings) > 0 && len(results) > 0 {
		if cr, ok := results[len(results)-1].(*ConnectionApplyResult); ok {
			cr.Warnings = resultWarnings
		}
	}

	return results, nil
}

// applyGCPMonitoringConfig applies GCP monitoring configuration
func (a *Applier) applyGCPMonitoringConfig(data []byte) (ApplyResult, error) {
	handler := gcpmonitoringconfig.NewHandler(a.client)

	var config gcpmonitoringconfig.GCPMonitoringConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse GCP monitoring config JSON: %w", err)
	}

	objectID := config.ObjectID

	if config.Value.Version == "" && config.Version != "" {
		config.Value.Version = config.Version
	}

	var warnings []string

	if objectID == "" && config.Value.Description != "" {
		existing, err := handler.FindByName(config.Value.Description)
		if err != nil && !errors.Is(err, gcpmonitoringconfig.ErrNotFound) {
			return nil, nameLookupError("GCP monitoring config", config.Value.Description, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing GCP monitoring config %q with ID: %s", config.Value.Description, existing.ObjectID)
			objectID = existing.ObjectID
			config.ObjectID = objectID
		}
	}

	if objectID == "" {
		if config.Value.Version == "" {
			latestVersion, err := handler.GetLatestVersion()
			if err != nil {
				return nil, fmt.Errorf("failed to determine extension version for gcp_monitoring_config: %w", err)
			}
			config.Value.Version = latestVersion
			config.Version = latestVersion
			a.stderrWarn(&warnings, "Using latest extension version: %s", latestVersion)
		}

		cleanData, err := json.Marshal(config)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal clean config: %w", err)
		}

		res, err := handler.Create(cleanData)
		if err != nil {
			return nil, err
		}
		return &MonitoringConfigApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "gcp_monitoring_config",
				ID:           res.ObjectID,
				Name:         config.Value.Description,
				Warnings:     warnings,
			},
			Scope: config.Scope,
		}, nil
	}

	if config.Value.Version == "" {
		existing, err := handler.Get(objectID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch existing config to preserve version: %w", err)
		}
		a.stderrWarn(&warnings, "Preserving existing version: %s", existing.Value.Version)
		config.Value.Version = existing.Value.Version
		config.Version = existing.Value.Version
	}

	cleanData, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clean config: %w", err)
	}

	res, err := handler.Update(objectID, cleanData)
	if err != nil {
		return nil, err
	}
	return &MonitoringConfigApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "gcp_monitoring_config",
			ID:           res.ObjectID,
			Name:         config.Value.Description,
			Warnings:     warnings,
		},
		Scope: config.Scope,
	}, nil
}

// dryRunGCPConnection reports what an apply would do to a GCP connection,
// resolving create vs update exactly as applyGCPConnection does: the payload's
// objectId first, then a search of the live list by name and type (issue #509).
func (a *Applier) dryRunGCPConnection(item map[string]interface{}) (ApplyResult, error) {
	parsed, err := parseGCPConnectionItem(item)
	if err != nil {
		return nil, err
	}

	objectID := parsed.objectID

	if objectID == "" {
		handler := gcpconnection.NewHandler(a.client)
		existing, err := handler.FindByNameAndType(parsed.value.Name, parsed.value.Type)
		if err != nil {
			return nil, nameLookupError("GCP connection", parsed.value.Name, err)
		}
		if existing != nil {
			objectID = existing.ObjectID
		}
	}

	action := ActionCreated
	if objectID != "" {
		action = ActionUpdated
	}

	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: "gcp_connection",
			ID:           objectID,
			Name:         parsed.value.Name,
		},
		Scope: parsed.scope,
	}, nil
}

// dryRunGCPMonitoringConfig reports what an apply would do to a GCP monitoring
// configuration, resolving create vs update exactly as
// applyGCPMonitoringConfig does (issue #509).
func (a *Applier) dryRunGCPMonitoringConfig(data []byte) (ApplyResult, error) {
	var config gcpmonitoringconfig.GCPMonitoringConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse GCP monitoring config JSON: %w", err)
	}

	objectID := config.ObjectID
	var warnings []string

	if objectID == "" && config.Value.Description != "" {
		handler := gcpmonitoringconfig.NewHandler(a.client)
		existing, err := handler.FindByName(config.Value.Description)
		if err != nil && !errors.Is(err, gcpmonitoringconfig.ErrNotFound) {
			return nil, nameLookupError("GCP monitoring config", config.Value.Description, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing GCP monitoring config %q with ID: %s", config.Value.Description, existing.ObjectID)
			objectID = existing.ObjectID
		}
	}

	return monitoringConfigDryRun("gcp_monitoring_config", objectID, config.Value.Description, config.Scope, warnings), nil
}
