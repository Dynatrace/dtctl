package apply

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
)

// azureConnectionItem holds the envelope fields an Azure connection payload
// carries around its value. Parsing lives in one place so that apply and dry
// run resolve the same object from the same document (issue #509).
type azureConnectionItem struct {
	objectID string
	schemaID string
	scope    string
	value    azureconnection.Value
}

// parseAzureConnectionItem reads one Azure connection payload. Both the API
// spelling ("objectId", "schemaId") and the YAML round-trip spelling
// ("objectid", "schemaid") are accepted, and an absent scope defaults to
// "environment" exactly as the apply path assumes.
func parseAzureConnectionItem(item map[string]interface{}) (azureConnectionItem, error) {
	parsed := azureConnectionItem{
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
		return parsed, fmt.Errorf("azure connection missing 'value' field")
	}

	// Convert valueMap to Value struct
	valueJSON, err := json.Marshal(valueMap)
	if err != nil {
		return parsed, fmt.Errorf("failed to marshal value: %w", err)
	}
	if err := json.Unmarshal(valueJSON, &parsed.value); err != nil {
		return parsed, fmt.Errorf("failed to unmarshal value: %w", err)
	}

	return parsed, nil
}

// applyAzureConnection applies Azure connection (credential)
func (a *Applier) applyAzureConnection(data []byte) ([]ApplyResult, error) {
	// Azure connection input might be a single object or a list of setting objects
	var items []map[string]interface{}

	// Try parsing as array first
	err := json.Unmarshal(data, &items)
	if err != nil {
		// Not an array, try parsing as single object
		var item map[string]interface{}
		if errSingle := json.Unmarshal(data, &item); errSingle != nil {
			return nil, fmt.Errorf("failed to parse Azure connection JSON: %w", errSingle)
		}
		items = []map[string]interface{}{item}
	}

	handler := azureconnection.NewHandler(a.client)

	var results []ApplyResult
	var resultWarnings []string
	for _, item := range items {
		parsed, err := parseAzureConnectionItem(item)
		if err != nil {
			return nil, err
		}
		objectID, schemaID, scope, value := parsed.objectID, parsed.schemaID, parsed.scope, parsed.value

		// Auto-lookup for Federated Credentials if ObjectID is missing
		if objectID == "" && value.Type == azureconnection.TypeFederatedIdentityCredential {
			existing, err := handler.FindByNameAndType(value.Name, value.Type)
			if err != nil {
				return nil, nameLookupError("Azure connection", value.Name, err)
			}
			if existing != nil {
				objectID = existing.ObjectID
				a.stderrWarn(&resultWarnings, "Found existing Federated Credential connection %q (ID: %s), switching to update mode", value.Name, objectID)
			}
		}

		// Read optional issuer override — lets callers specify a custom token issuer
		// in the YAML without requiring auto-detection from the host name.
		issuerOverride, _ := item["issuer"].(string)

		if objectID == "" {
			// Create
			req := azureconnection.AzureConnectionCreate{
				SchemaID: schemaID,
				Scope:    scope,
				Value:    value,
			}
			res, err := handler.Create(req)
			if err != nil {
				return nil, fmt.Errorf("failed to create Azure connection: %w", err)
			}

			// Check for federated identity to print instructions
			if value.Type == "federatedIdentityCredential" {
				a.printFederatedInstructions(a.baseURL, res.ObjectID, issuerOverride, &resultWarnings)
			}

			results = append(results, &ConnectionApplyResult{
				ApplyResultBase: ApplyResultBase{
					Action:       ActionCreated,
					ResourceType: "azure_connection",
					ID:           res.ObjectID,
					Name:         value.Name,
				},
				SchemaID: schemaID,
				Scope:    scope,
			})
		} else {
			// Update
			_, err := handler.Update(objectID, value)
			if err != nil {
				errMsg := err.Error()

				// Catch generic validation error that happens when Azure side is not ready/configured
				// "was unable to be validated with validator .../azureConfiguration"
				if strings.Contains(errMsg, "azureConfiguration") && strings.Contains(errMsg, "unable to be validated") {
					// Check if we have incomplete configuration (missing app/directory ID)
					if value.Type == "federatedIdentityCredential" {
						fedCred := value.FederatedIdentityCredential
						if fedCred == nil || fedCred.ApplicationID == "" || fedCred.DirectoryID == "" {
							a.printFederatedCompleteInstructions(a.baseURL, objectID, value.Name, issuerOverride)
							return nil, fmt.Errorf("azure connection requires additional configuration: %w", err)
						}
					}
				}

				// Check for Federated Identity error (AADSTS70025 or AADSTS700213)
				if strings.Contains(errMsg, "AADSTS70025") || strings.Contains(errMsg, "AADSTS700213") {
					if value.FederatedIdentityCredential != nil && value.FederatedIdentityCredential.ApplicationID != "" {
						a.printFederatedErrorSnippet(a.baseURL, objectID, value.FederatedIdentityCredential.ApplicationID, issuerOverride)
						return nil, fmt.Errorf("azure connection requires federation setup on Azure side: %w", err)
					}
				}
				return nil, fmt.Errorf("failed to update Azure connection %s: %w", objectID, err)
			}

			results = append(results, &ConnectionApplyResult{
				ApplyResultBase: ApplyResultBase{
					Action:       ActionUpdated,
					ResourceType: "azure_connection",
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

// applyAzureMonitoringConfig applies Azure monitoring configuration
func (a *Applier) applyAzureMonitoringConfig(data []byte) (ApplyResult, error) {
	handler := azuremonitoringconfig.NewHandler(a.client)

	// Unmarshal to struct to handle casing properly via json tags
	var config azuremonitoringconfig.AzureMonitoringConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse Azure monitoring config JSON: %w", err)
	}

	objectID := config.ObjectID

	if config.Value.Version == "" && config.Version != "" {
		config.Value.Version = config.Version
	}

	var warnings []string

	// Lookup by name if ID is missing (Feature 1: naming convention lookup)
	if objectID == "" && config.Value.Description != "" {
		existing, err := handler.FindByName(config.Value.Description)
		if err != nil && !errors.Is(err, azuremonitoringconfig.ErrNotFound) {
			return nil, nameLookupError("Azure monitoring config", config.Value.Description, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing Azure monitoring config %q with ID: %s", config.Value.Description, existing.ObjectID)
			objectID = existing.ObjectID
			config.ObjectID = objectID // Set ID for update
		}
	}

	if objectID == "" {
		if config.Value.Version == "" {
			latestVersion, err := handler.GetLatestVersion()
			if err != nil {
				return nil, fmt.Errorf("failed to determine extension version for azure_monitoring_config: %w", err)
			}
			config.Value.Version = latestVersion
			config.Version = latestVersion
			a.stderrWarn(&warnings, "Using latest extension version: %s", latestVersion)
		}

		// New creation
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
				ResourceType: "azure_monitoring_config",
				ID:           res.ObjectID,
				Name:         config.Value.Description,
				Warnings:     warnings,
			},
			Scope: config.Scope,
		}, nil
	}

	// Update existing

	// Feature 2: If version is missing in YAML, preserve existing version
	if config.Value.Version == "" {
		existing, err := handler.Get(objectID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch existing config to preserve version: %w", err)
		} else {
			a.stderrWarn(&warnings, "Preserving existing version: %s", existing.Value.Version)
			config.Value.Version = existing.Value.Version
			config.Version = existing.Value.Version
		}
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
			ResourceType: "azure_monitoring_config",
			ID:           res.ObjectID,
			Name:         config.Value.Description,
			Warnings:     warnings,
		},
		Scope: config.Scope,
	}, nil
}

// printFederatedInstructions prints configuration instructions for Federated Identity Credential to stderr
func (a *Applier) printFederatedInstructions(baseURL, objectID, issuerOverride string, warnings *[]string) {
	u, err := url.Parse(baseURL)
	if err != nil {
		// Should not happen if client is initialized correctly, but fail gracefully
		output.FprintWarning(a.stderrW(), "Could not parse base URL for instructions: %v", err)
		return
	}
	host := u.Host

	issuer := issuerOverride
	if issuer == "" {
		issuer = azureconnection.TokenIssuerForHost(host)
	}

	fmt.Fprintf(a.stderrW(), "\nFurther configuration required in Azure Portal (Federated Credentials):\n")
	fmt.Fprintf(a.stderrW(), "  Issuer:    %s\n", issuer)
	fmt.Fprintf(a.stderrW(), "  Subject:   dt:connection-id/%s\n", objectID)
	fmt.Fprintf(a.stderrW(), "  Audiences: %s/svc-id/com.dynatrace.da\n", host)

	if warnings != nil {
		*warnings = append(*warnings, "Azure federated credential requires additional portal setup")
	}
}

// printFederatedCompleteInstructions prints full configuration instructions for Federated Identity Credential to stderr
func (a *Applier) printFederatedCompleteInstructions(baseURL, objectID, connectionName, issuerOverride string) {
	u, err := url.Parse(baseURL)
	if err != nil {
		output.FprintWarning(a.stderrW(), "Could not parse base URL for instructions: %v", err)
		return
	}
	host := u.Host

	issuer := issuerOverride
	if issuer == "" {
		issuer = azureconnection.TokenIssuerForHost(host)
	}

	fmt.Fprintf(a.stderrW(), "\nTo complete the configuration, additional setup is required in the Azure Portal (Federated Credentials).\n")
	fmt.Fprintf(a.stderrW(), "Details for Azure configuration:\n")
	fmt.Fprintf(a.stderrW(), "  Issuer:    %s\n", issuer)
	fmt.Fprintf(a.stderrW(), "  Subject:   dt:connection-id/%s\n", objectID)
	fmt.Fprintf(a.stderrW(), "  Audiences: %s/svc-id/com.dynatrace.da\n", host)
	fmt.Fprintln(a.stderrW())
	fmt.Fprintf(a.stderrW(), "Azure CLI commands:\n")
	fmt.Fprintf(a.stderrW(), "1. Create Service Principal (if not created yet):\n")
	fmt.Fprintf(a.stderrW(), "   az ad sp create-for-rbac --name %q --create-password false --query \"{CLIENT_ID:appId, TENANT_ID:tenant}\" --output table", connectionName)
	fmt.Fprintln(a.stderrW())
	fmt.Fprintf(a.stderrW(), "2. Create Federated Credential:\n")
	fmt.Fprintf(a.stderrW(), "   az ad app federated-credential create --id \"<CLIENT_ID>\" --parameters \"{'name': 'fd-Federated-Credential', 'issuer': '%s', 'subject': 'dt:connection-id/%s', 'audiences': ['%s/svc-id/com.dynatrace.da']}\"\n", issuer, objectID, host)
	fmt.Fprintln(a.stderrW())
}

// printFederatedErrorSnippet prints az cli snippet for AADSTS70025 error to stderr
func (a *Applier) printFederatedErrorSnippet(baseURL, objectID, clientID, issuerOverride string) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return
	}
	host := u.Host

	issuer := issuerOverride
	if issuer == "" {
		issuer = azureconnection.TokenIssuerForHost(host)
	}

	fmt.Fprintf(a.stderrW(), "\nTo fix the Federated Identity error, run the following command:\n")
	// Use format validated by user: "{'key': 'value'}"
	fmt.Fprintf(a.stderrW(), "az ad app federated-credential create --id %q --parameters \"{'name': 'fd-Federated-Credential', 'issuer': '%s', 'subject': 'dt:connection-id/%s', 'audiences': ['%s/svc-id/com.dynatrace.da']}\"\n", clientID, issuer, objectID, host)
	fmt.Fprintln(a.stderrW())
}

// dryRunAzureConnection reports what an apply would do to an Azure connection.
// It repeats applyAzureConnection's create-vs-update resolution: the payload's
// objectId first, then — for federated credentials, the only type apply looks
// up — a search of the live list by name and type (issue #509).
func (a *Applier) dryRunAzureConnection(item map[string]interface{}) (ApplyResult, error) {
	parsed, err := parseAzureConnectionItem(item)
	if err != nil {
		return nil, err
	}

	objectID := parsed.objectID
	var warnings []string

	if objectID == "" && parsed.value.Type == azureconnection.TypeFederatedIdentityCredential {
		handler := azureconnection.NewHandler(a.client)
		existing, err := handler.FindByNameAndType(parsed.value.Name, parsed.value.Type)
		if err != nil {
			return nil, nameLookupError("Azure connection", parsed.value.Name, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing Federated Credential connection %q (ID: %s), switching to update mode", parsed.value.Name, existing.ObjectID)
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
			ResourceType: "azure_connection",
			ID:           objectID,
			Name:         parsed.value.Name,
			Warnings:     warnings,
		},
		Scope: parsed.scope,
	}, nil
}

// dryRunAzureMonitoringConfig reports what an apply would do to an Azure
// monitoring configuration, resolving create vs update exactly as
// applyAzureMonitoringConfig does (issue #509).
func (a *Applier) dryRunAzureMonitoringConfig(data []byte) (ApplyResult, error) {
	var config azuremonitoringconfig.AzureMonitoringConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse Azure monitoring config JSON: %w", err)
	}

	objectID := config.ObjectID
	var warnings []string

	if objectID == "" && config.Value.Description != "" {
		handler := azuremonitoringconfig.NewHandler(a.client)
		existing, err := handler.FindByName(config.Value.Description)
		if err != nil && !errors.Is(err, azuremonitoringconfig.ErrNotFound) {
			return nil, nameLookupError("Azure monitoring config", config.Value.Description, err)
		}
		if existing != nil {
			a.stderrWarn(&warnings, "Found existing Azure monitoring config %q with ID: %s", config.Value.Description, existing.ObjectID)
			objectID = existing.ObjectID
		}
	}

	return monitoringConfigDryRun("azure_monitoring_config", objectID, config.Value.Description, config.Scope, warnings), nil
}
