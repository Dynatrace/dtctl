package gcpmonitoringconfig

import (
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/resources/gcpconnection"
)

func ResolveCredential(identifier string, handler *gcpconnection.Handler) (Credential, error) {
	item, err := handler.FindByName(identifier)
	if err != nil {
		if !isNameNotFoundError(err) {
			return Credential{}, fmt.Errorf("failed to resolve gcp connection %q by name: %w", identifier, err)
		}

		item, err = handler.Get(identifier)
		if err != nil {
			if isNotFoundError(err) {
				return Credential{}, fmt.Errorf("gcp connection %q not found by name or ID", identifier)
			}
			return Credential{}, fmt.Errorf("failed to resolve gcp connection %q by name or ID: %w", identifier, err)
		}
	}

	serviceAccount := ""
	if item.Value.ServiceAccountImpersonation != nil {
		serviceAccount = item.Value.ServiceAccountImpersonation.ServiceAccountID
	}

	return Credential{
		Enabled:        true,
		Description:    item.Name,
		ConnectionID:   item.ObjectID,
		ServiceAccount: serviceAccount,
	}, nil
}

func isNameNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "gcp connection with name") && strings.Contains(msg, "not found")
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// AllLocations is the --locationFiltering value that removes the location
// filter, so every location is monitored.
const AllLocations = "all"

// ParseLocations parses a --locationFiltering value into the config's
// locationFiltering list. A blank input and AllLocations both yield an empty
// list, which the backend reads as "no location filter". Spelling out every
// schema location instead is not equivalent: the Smartscape poller turns each
// listed location into an alternation of one Cloud Asset query, and GCP
// rejects the query once the list is long ("Query has too many alternations").
func ParseLocations(input string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return []string{}, nil
	}

	locations := SplitCSV(input)
	if len(locations) == 0 {
		return nil, fmt.Errorf("--locationFiltering must contain at least one location, or %q", AllLocations)
	}
	for _, location := range locations {
		if strings.EqualFold(location, AllLocations) {
			if len(locations) > 1 {
				return nil, fmt.Errorf("--locationFiltering %q cannot be combined with other locations", AllLocations)
			}
			return []string{}, nil
		}
	}
	return locations, nil
}

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
