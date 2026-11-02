package azuremonitoringconfig

import (
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/resources/azureconnection"
)

// ResolveCredential resolves Azure connection by name or ID and maps it to monitoring credential structure.
func ResolveCredential(identifier string, handler *azureconnection.Handler) (Credential, error) {
	item, err := handler.FindByName(identifier)
	if err != nil {
		item, err = handler.Get(identifier)
		if err != nil {
			return Credential{}, fmt.Errorf("azure connection %q not found by name or ID", identifier)
		}
	}

	credentialType := "FEDERATED"
	servicePrincipalID := ""
	if item.Value.Type == "clientSecret" {
		credentialType = "SECRET"
		if item.Value.ClientSecret != nil {
			servicePrincipalID = item.Value.ClientSecret.ApplicationID
		}
	} else if item.Value.Type == "federatedIdentityCredential" {
		if item.Value.FederatedIdentityCredential != nil {
			servicePrincipalID = item.Value.FederatedIdentityCredential.ApplicationID
		}
	}

	return Credential{
		Enabled:            true,
		Description:        item.Name,
		ConnectionId:       item.ObjectID,
		ServicePrincipalId: servicePrincipalID,
		Type:               credentialType,
	}, nil
}

// ParseOrDefaultLocations parses CSV locations or returns all available locations from schema.
func ParseOrDefaultLocations(input string, handler *Handler) ([]string, error) {
	if strings.TrimSpace(input) != "" {
		return SplitCSV(input), nil
	}

	available, err := handler.ListAvailableLocations()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(available))
	for _, location := range available {
		out = append(out, location.Value)
	}
	return out, nil
}

// ParseOrDefaultFeatureSets parses CSV feature sets or returns all *_essential feature sets from schema.
func ParseOrDefaultFeatureSets(input string, handler *Handler) ([]string, error) {
	if strings.TrimSpace(input) != "" {
		return SplitCSV(input), nil
	}

	available, err := handler.ListAvailableFeatureSets()
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(available))
	for _, featureSet := range available {
		if strings.HasSuffix(featureSet.Value, "_essential") {
			out = append(out, featureSet.Value)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no feature sets with suffix _essential found")
	}
	return out, nil
}

// ParseTagFiltering parses tag filtering expression like:
// include:key=value,key2=value2;exclude:key3=value3
func ParseTagFiltering(input string) ([]TagFilter, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	sections := strings.Split(trimmed, ";")
	tagFilters := make([]TagFilter, 0)

	for _, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}

		parts := strings.SplitN(section, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid tagfiltering section %q (expected format include:key=value)", section)
		}

		conditionRaw := strings.ToLower(strings.TrimSpace(parts[0]))
		condition := ""
		switch conditionRaw {
		case "include":
			condition = "INCLUDE"
		case "exclude", "eclude":
			condition = "EXCLUDE"
		default:
			return nil, fmt.Errorf("invalid tagfiltering condition %q (expected include/exclude)", conditionRaw)
		}

		pairs := SplitCSV(parts[1])
		for _, pair := range pairs {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				return nil, fmt.Errorf("invalid tagfiltering pair %q (expected key=value)", pair)
			}
			tagFilters = append(tagFilters, TagFilter{
				Key:       strings.TrimSpace(kv[0]),
				Value:     strings.TrimSpace(kv[1]),
				Condition: condition,
			})
		}
	}

	return tagFilters, nil
}

// SplitCSV splits comma-separated values and trims spaces.
func SplitCSV(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
