package apply

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
)

// applyAWSMonitoringConfig applies AWS monitoring configuration
func (a *Applier) applyAWSMonitoringConfig(data []byte) (ApplyResult, error) {
	handler := awsmonitoringconfig.NewHandler(a.client)

	var config awsmonitoringconfig.AWSMonitoringConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse AWS monitoring config JSON: %w", err)
	}

	objectID := config.ObjectID

	if config.Value.Version == "" && config.Version != "" {
		config.Value.Version = config.Version
	}

	var warnings []string

	if objectID == "" && config.Value.Description != "" {
		existing, err := handler.FindByName(config.Value.Description)
		if err != nil && !errors.Is(err, awsmonitoringconfig.ErrNotFound) {
			return nil, nameLookupError("AWS monitoring config", config.Value.Description, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing AWS monitoring config %q with ID: %s", config.Value.Description, existing.ObjectID)
			objectID = existing.ObjectID
			config.ObjectID = objectID
		}
	}

	if objectID == "" {
		if config.Value.Version == "" {
			latestVersion, err := handler.GetLatestVersion()
			if err != nil {
				return nil, fmt.Errorf("failed to determine extension version for aws_monitoring_config: %w", err)
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
				ResourceType: "aws_monitoring_config",
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
			ResourceType: "aws_monitoring_config",
			ID:           res.ObjectID,
			Name:         config.Value.Description,
			Warnings:     warnings,
		},
		Scope: config.Scope,
	}, nil
}

// dryRunAWSMonitoringConfig reports what an apply would do to an AWS
// monitoring configuration. It repeats applyAWSMonitoringConfig's
// create-vs-update resolution — the payload's objectId first, then a lookup of
// the live list by description — so that the dry run cannot promise a create
// for a configuration the apply behind it overwrites (issue #509).
func (a *Applier) dryRunAWSMonitoringConfig(data []byte) (ApplyResult, error) {
	var config awsmonitoringconfig.AWSMonitoringConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse AWS monitoring config JSON: %w", err)
	}

	objectID := config.ObjectID
	var warnings []string

	if objectID == "" && config.Value.Description != "" {
		handler := awsmonitoringconfig.NewHandler(a.client)
		existing, err := handler.FindByName(config.Value.Description)
		if err != nil && !errors.Is(err, awsmonitoringconfig.ErrNotFound) {
			return nil, nameLookupError("AWS monitoring config", config.Value.Description, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing AWS monitoring config %q with ID: %s", config.Value.Description, existing.ObjectID)
			objectID = existing.ObjectID
		}
	}

	return monitoringConfigDryRun("aws_monitoring_config", objectID, config.Value.Description, config.Scope, warnings), nil
}
