package apply

import (
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/resources/segment"
	"github.com/Dynatrace/dtctl/pkg/safety"
)

// applySegment applies a filter segment resource
func (a *Applier) applySegment(data []byte) (ApplyResult, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse segment JSON: %w", err)
	}

	handler := segment.NewHandler(a.client)

	uid, hasUID := raw["uid"].(string)
	if !hasUID || uid == "" {
		// Create new segment
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create segment: %w", err)
		}
		return &SegmentApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "segment",
				ID:           result.UID,
				Name:         result.Name,
			},
		}, nil
	}

	// Check if segment exists
	existing, err := handler.Get(uid)
	if err != nil {
		// Only fall through to create if the error is a 404 (not found).
		// Other errors (network, 500, 403) should be surfaced immediately.
		// segment wraps its own sentinel, so the shared helper cannot see it.
		if !segment.IsNotFound(err) {
			return nil, nameLookupError("segment", uid, err)
		}

		// Segment doesn't exist, create it
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create segment: %w", err)
		}
		return &SegmentApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "segment",
				ID:           result.UID,
				Name:         result.Name,
			},
		}, nil
	}

	// Safety check for update operation - determine ownership from existing segment
	ownership := a.determineOwnership(existing.Owner)
	if err := a.checkSafety(safety.OperationUpdate, ownership); err != nil {
		return nil, err
	}

	// Update existing segment
	if err := handler.Update(uid, existing.Version, data); err != nil {
		return nil, fmt.Errorf("failed to update segment: %w", err)
	}

	name, _ := raw["name"].(string)
	return &SegmentApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "segment",
			ID:           uid,
			Name:         name,
		},
	}, nil
}
