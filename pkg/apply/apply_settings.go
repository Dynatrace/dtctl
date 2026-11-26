package apply

import (
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/resources/settings"
	"github.com/Dynatrace/dtctl/pkg/safety"
)

// settingsPlan is the outcome of resolving a settings payload against the
// environment: which object it addresses and whether the apply creates or
// updates it. applySettings and dryRunSettings share it so a dry run cannot
// report an action the apply behind it would not take.
type settingsPlan struct {
	objectID string
	schemaID string
	scope    string
	value    map[string]interface{}
	// existing is the live object objectID names, nil when the apply creates.
	existing *settings.SettingsObject
}

// resolveSettings parses a settings payload and decides create vs update.
// It issues at most one read-only GET (the objectId lookup) and returns the
// same error the apply would when neither an update nor a create is possible.
func (a *Applier) resolveSettings(handler *settings.Handler, data []byte) (*settingsPlan, error) {
	var setting map[string]interface{}
	if err := json.Unmarshal(data, &setting); err != nil {
		return nil, fmt.Errorf("failed to parse settings JSON: %w", err)
	}

	// Extract fields - handle both camelCase (API format) and lowercase (YAML keys).
	// Also accept "id" as a final fallback so that the --id CLI flag (which injects
	// doc["id"] via injectID) works for settings objects the same way it does for
	// other resource types. See https://github.com/Dynatrace/dtctl/issues/255
	objectID := objectIDFromDoc(setting)
	if objectID == "" {
		objectID, _ = setting["id"].(string)
	}

	schemaID, _ := setting["schemaId"].(string)
	if schemaID == "" {
		schemaID, _ = setting["schemaid"].(string)
	}

	scope, _ := setting["scope"].(string)

	value, ok := setting["value"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("settings object missing 'value' field or value is not an object")
	}

	plan := &settingsPlan{objectID: objectID, schemaID: schemaID, scope: scope, value: value}

	// No objectID: create a new settings object.
	if objectID == "" {
		if schemaID == "" {
			return nil, fmt.Errorf("schemaId is required to create a settings object")
		}
		if scope == "" {
			return nil, fmt.Errorf("scope is required to create a settings object")
		}
		return plan, nil
	}

	// Check if settings object exists
	existing, err := handler.Get(objectID)
	if err != nil {
		if lookupErr := lookupError("settings object", objectID, err); lookupErr != nil {
			return nil, lookupErr
		}

		// Doesn't exist - fall back to a create, which needs schemaId and scope.
		if schemaID == "" {
			return nil, fmt.Errorf("schemaId is required to create a settings object (objectId %q not found)", objectID)
		}
		if scope == "" {
			return nil, fmt.Errorf("scope is required to create a settings object (objectId %q not found)", objectID)
		}
		return plan, nil
	}

	plan.existing = existing
	return plan, nil
}

// applySettings applies a settings object resource
func (a *Applier) applySettings(data []byte) (ApplyResult, error) {
	handler := settings.NewHandler(a.client)

	plan, err := a.resolveSettings(handler, data)
	if err != nil {
		return nil, err
	}

	if plan.existing == nil {
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		req := settings.SettingsObjectCreate{
			SchemaID: plan.schemaID,
			Scope:    plan.scope,
			Value:    plan.value,
		}

		result, err := handler.Create(req)
		if err != nil {
			return nil, fmt.Errorf("failed to create settings object: %w", err)
		}

		return &SettingsApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "settings",
				ID:           result.ObjectID,
			},
			SchemaID: plan.schemaID,
			Scope:    plan.scope,
		}, nil
	}

	// Update existing settings object
	if err := a.checkSafety(safety.OperationUpdate, safety.OwnershipUnknown); err != nil {
		return nil, err
	}

	updated, err := handler.Update(plan.objectID, plan.value)
	if err != nil {
		return nil, fmt.Errorf("failed to update settings object: %w", err)
	}

	return &SettingsApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "settings",
			ID:           updated.ObjectID,
			Name:         updated.Summary,
		},
		SchemaID: updated.SchemaID,
		Scope:    updated.Scope,
		Summary:  updated.Summary,
	}, nil
}

// dryRunSettings reports what an apply would do to a settings object,
// resolving create vs update exactly as applySettings does: an objectId that
// names no existing object is a create, and fails when the payload lacks the
// schemaId and scope a create needs. See
// https://github.com/Dynatrace/dtctl/issues/521
func (a *Applier) dryRunSettings(doc map[string]interface{}, data []byte) (ApplyResult, error) {
	plan, err := a.resolveSettings(settings.NewHandler(a.client), data)
	if err != nil {
		return nil, err
	}

	name, _ := doc["name"].(string)
	if name == "" {
		name, _ = doc["title"].(string)
	}

	action := ActionCreated
	id := ""
	scope := plan.scope
	if plan.existing != nil {
		// Report the live object the update targets, as the apply result
		// does: a legacy export carries no scope, and its summary is the
		// name the update reports.
		action = ActionUpdated
		id = plan.objectID
		scope = plan.existing.Scope
		if plan.existing.Summary != "" {
			name = plan.existing.Summary
		}
	}

	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: string(ResourceSettings),
			ID:           id,
			Name:         name,
		},
		Scope: scope,
	}, nil
}
