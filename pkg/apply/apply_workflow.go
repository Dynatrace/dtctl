package apply

import (
	"encoding/json"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

// applyWorkflow applies a workflow resource
func (a *Applier) applyWorkflow(data []byte, opts ApplyOptions) (ApplyResult, error) {
	// Parse to check for ID
	var wf map[string]interface{}
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("failed to parse workflow JSON: %w", err)
	}

	handler := workflow.NewHandler(a.client)

	id, hasID := wf["id"].(string)
	if !hasID || id == "" {
		// Create new workflow
		// Safety check for create operation
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create workflow: %w", err)
		}

		var warnings []string
		// File had no id field before this apply — stamp it back or hint.
		a.applyWriteBack(a.sourceFile, result.ID, "workflow", opts.WriteID, false, &warnings)

		return &WorkflowApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "workflow",
				ID:           result.ID,
				Name:         result.Title,
				Warnings:     warnings,
			},
		}, nil
	}

	// Check if workflow exists
	existing, err := handler.Get(id)
	if err != nil {
		if lookupErr := lookupError("workflow", id, err); lookupErr != nil {
			return nil, lookupErr
		}

		// Workflow doesn't exist, create it
		// Safety check for create operation
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create workflow: %w", err)
		}

		var warnings []string
		// File already had an id field (we got here because the resource wasn't found).
		// No stamp or hint needed — the file is already self-contained.
		a.applyWriteBack(a.sourceFile, result.ID, "workflow", opts.WriteID, true, &warnings)

		return &WorkflowApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "workflow",
				ID:           result.ID,
				Name:         result.Title,
				Warnings:     warnings,
			},
		}, nil
	}

	// Safety check for update operation - determine ownership from existing workflow
	ownership := a.determineOwnership(existing.Owner)
	if err := a.checkSafety(safety.OperationUpdate, ownership); err != nil {
		return nil, err
	}

	// Update existing workflow
	result, err := handler.Update(id, data)
	if err != nil {
		return nil, fmt.Errorf("failed to update workflow: %w", err)
	}

	return &WorkflowApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "workflow",
			ID:           result.ID,
			Name:         result.Title,
		},
	}, nil
}
