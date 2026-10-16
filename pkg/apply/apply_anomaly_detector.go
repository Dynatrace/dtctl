package apply

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

// applyAnomalyDetector applies a custom anomaly detector resource.
// Supports both flattened YAML format and raw Settings API format.
func (a *Applier) applyAnomalyDetector(data []byte) (ApplyResult, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse anomaly detector JSON: %w", err)
	}

	handler := anomalydetector.NewHandler(a.client).WithDefaultActor(a.currentUserID)

	// Extract object ID (present in raw Settings format or if user includes it)
	objectID, _ := raw["objectId"].(string)
	if objectID == "" {
		objectID, _ = raw["objectid"].(string)
	}

	// If no objectId, try to find existing detector by title for idempotent apply
	if objectID == "" {
		title := anomalydetector.ExtractTitle(data)
		if title != "" {
			existing, err := handler.FindByExactTitle(title)
			if err != nil {
				// A failed lookup says nothing about whether the detector exists.
				// Creating anyway adds a second detector with the same title.
				return nil, nameLookupError("anomaly detector", title, err)
			}
			if existing != nil {
				objectID = existing.ObjectID
				a.stderrWarn(nil, "Found existing anomaly detector %q (ID: %s), switching to update mode", title, objectID)
			}
		}
	}

	if objectID == "" {
		// No objectId and no existing match — create new anomaly detector
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create anomaly detector: %w", err)
		}
		return &AnomalyDetectorApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "anomaly_detector",
				ID:           result.ObjectID,
				Name:         result.Title,
			},
		}, nil
	}

	// objectId present — update existing anomaly detector
	if err := a.checkSafety(safety.OperationUpdate, safety.OwnershipUnknown); err != nil {
		return nil, err
	}

	result, err := handler.Update(objectID, data)
	if err != nil {
		return nil, fmt.Errorf("failed to update anomaly detector: %w", err)
	}

	return &AnomalyDetectorApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "anomaly_detector",
			ID:           result.ObjectID,
			Name:         result.Title,
		},
	}, nil
}

// dryRunAnomalyDetector reports what an apply would do to an anomaly detector
// and whether the definition is actually accepted. It runs the same local
// normalization as apply and then asks the Settings API to validate the payload
// without persisting it, so a dry run no longer reports success for a
// definition the live call rejects (issue #369).
func (a *Applier) dryRunAnomalyDetector(data []byte) (ApplyResult, error) {
	handler := anomalydetector.NewHandler(a.client).WithDefaultActor(a.currentUserID)

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse anomaly detector JSON: %w", err)
	}

	objectID, _ := raw["objectId"].(string)
	if objectID == "" {
		objectID, _ = raw["objectid"].(string)
	}

	title := anomalydetector.ExtractTitle(data)
	var warnings []string

	// Resolve create vs update the same way applyAnomalyDetector does.
	if objectID == "" && title != "" {
		existing, err := handler.FindByExactTitle(title)
		if err != nil {
			// The dry run resolves create vs update exactly as apply does, so it
			// cannot report a create that the apply behind it would refuse.
			return nil, nameLookupError("anomaly detector", title, err)
		}
		if existing != nil {
			objectID = existing.ObjectID
		}
	}

	action := ActionCreated
	var validationErr error
	if objectID == "" {
		validationErr = handler.ValidateCreate(data)
	} else {
		action = ActionUpdated
		validationErr = handler.ValidateUpdate(objectID, data)
	}

	// A payload the server rejects fails the dry run; an unreachable or
	// unauthorized validation endpoint only downgrades its confidence.
	var unavailable *anomalydetector.ValidationUnavailableError
	switch {
	case validationErr == nil:
	case errors.As(validationErr, &unavailable):
		warnings = append(warnings, fmt.Sprintf("schema validation skipped: %v", unavailable.Err))
	default:
		return nil, validationErr
	}

	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: "anomaly_detector",
			ID:           objectID,
			Name:         title,
		},
		Scope:           anomalydetector.Scope,
		ValidationWarns: warnings,
	}, nil
}
