package apply

import (
	"encoding/json"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/resources/schedulingrule"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

// applySchedulingRule applies a scheduling rule resource.
func (a *Applier) applySchedulingRule(data []byte, opts ApplyOptions) (ApplyResult, error) {
	var sr map[string]interface{}
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, fmt.Errorf("failed to parse scheduling rule JSON: %w", err)
	}

	handler := schedulingrule.NewHandler(a.client)

	id, hasID := sr["id"].(string)
	if !hasID || id == "" {
		// Create new scheduling rule.
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create scheduling rule: %w", err)
		}

		var warnings []string
		a.applyWriteBack(a.sourceFile, result.ID, "scheduling-rule", opts.WriteID, false, &warnings)

		return &SchedulingRuleApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "scheduling-rule",
				ID:           result.ID,
				Name:         result.Title,
				Warnings:     warnings,
			},
		}, nil
	}

	// Check if scheduling rule exists.
	existing, err := handler.Get(id)
	if err != nil {
		// Only a genuine 404 may fall through to create. Treating every error as
		// "absent" would turn a 403 on someone else's rule into an unowned create,
		// bypassing the OwnershipOther check the update path applies.
		if lookupErr := lookupError("scheduling rule", id, err); lookupErr != nil {
			return nil, lookupErr
		}

		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create scheduling rule: %w", err)
		}

		var warnings []string
		a.applyWriteBack(a.sourceFile, result.ID, "scheduling-rule", opts.WriteID, true, &warnings)

		return &SchedulingRuleApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "scheduling-rule",
				ID:           result.ID,
				Name:         result.Title,
				Warnings:     warnings,
			},
		}, nil
	}

	// Safety check for update — determine ownership from existing resource.
	ownership := a.determineOwnership(existing.OwnerID())
	if err := a.checkSafety(safety.OperationUpdate, ownership); err != nil {
		return nil, err
	}

	result, err := handler.Update(id, data)
	if err != nil {
		return nil, fmt.Errorf("failed to update scheduling rule: %w", err)
	}

	return &SchedulingRuleApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "scheduling-rule",
			ID:           result.ID,
			Name:         result.Title,
		},
	}, nil
}
