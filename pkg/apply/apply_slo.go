package apply

import (
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/resources/slo"
)

// applySLO applies an SLO resource
func (a *Applier) applySLO(data []byte) (ApplyResult, error) {
	// Parse to check for ID
	var s map[string]interface{}
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to parse SLO JSON: %w", err)
	}

	handler := slo.NewHandler(a.client)

	id, hasID := s["id"].(string)
	if !hasID || id == "" {
		// Create new SLO
		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create SLO: %w", err)
		}
		return &SLOApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "slo",
				ID:           result.ID,
				Name:         result.Name,
			},
		}, nil
	}

	// Check if SLO exists
	existing, err := handler.Get(id)
	if err != nil {
		if lookupErr := lookupError("SLO", id, err); lookupErr != nil {
			return nil, lookupErr
		}

		// SLO doesn't exist, create it
		result, err := handler.Create(data)
		if err != nil {
			return nil, fmt.Errorf("failed to create SLO: %w", err)
		}
		return &SLOApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "slo",
				ID:           result.ID,
				Name:         result.Name,
			},
		}, nil
	}

	// Update existing SLO
	if err := handler.Update(id, existing.Version, data); err != nil {
		return nil, fmt.Errorf("failed to update SLO: %w", err)
	}

	name, _ := s["name"].(string)
	return &SLOApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "slo",
			ID:           id,
			Name:         name,
		},
	}, nil
}
