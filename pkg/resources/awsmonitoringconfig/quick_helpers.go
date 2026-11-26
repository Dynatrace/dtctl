package awsmonitoringconfig

import (
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
)

// ResolveCredential resolves an AWS connection by name or ID and returns
// a Credential entry suitable for an AWS monitoring configuration.
// AccountID is parsed from the ARN when present.
func ResolveCredential(identifier string, handler *awsconnection.Handler) (Credential, error) {
	item, err := handler.FindByName(identifier)
	if err != nil {
		item, err = handler.Get(identifier)
		if err != nil {
			return Credential{}, fmt.Errorf("aws connection %q not found by name or ID", identifier)
		}
	}

	roleArn := ""
	if item.Value.AwsRoleBasedAuthentication != nil {
		roleArn = item.Value.AwsRoleBasedAuthentication.RoleArn
	}

	return Credential{
		Enabled:      true,
		Description:  item.Name,
		ConnectionID: item.ObjectID,
		AccountID:    AccountIDFromRoleArn(roleArn),
	}, nil
}

// AccountIDFromRoleArn extracts the 12-digit account number from a role ARN
// like "arn:aws:iam::123456789012:role/Name". Returns empty string when not parseable.
func AccountIDFromRoleArn(arn string) string {
	if arn == "" {
		return ""
	}
	const prefix = "arn:aws:iam::"
	if !strings.HasPrefix(arn, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(arn, prefix)
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return ""
	}
	return rest[:idx]
}

// ParseRequiredRegions parses a CSV list of regions. Returns an error when input is empty.
func ParseRequiredRegions(input string) ([]string, error) {
	regions := SplitCSV(input)
	if len(regions) == 0 {
		return nil, fmt.Errorf("--regions is required (comma-separated AWS regions)")
	}
	return regions, nil
}

// ParseOrDefaultFeatureSets parses a CSV list or returns all *_essential
// feature sets discovered in the latest extension schema.
func ParseOrDefaultFeatureSets(input string, handler *Handler) ([]string, error) {
	if strings.TrimSpace(input) != "" {
		return SplitCSV(input), nil
	}

	available, err := handler.ListAvailableFeatureSets()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(available))
	for _, fs := range available {
		if strings.HasSuffix(fs.Value, "_essential") {
			out = append(out, fs.Value)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no feature sets with suffix _essential found")
	}
	return out, nil
}

// SplitCSV trims and removes empty entries.
func SplitCSV(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
