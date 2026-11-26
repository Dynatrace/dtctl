//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/apply"
	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/azureconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/gcpconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/util/format"
)

// cloudSpec describes one hyperscaler so the monitoring-config tests can run
// the same assertions for aws, azure and gcp without a switch per test.
type cloudSpec struct {
	// Name is the CLI provider word ("aws", "azure", "gcp") and the key into
	// knownFidelityGaps.
	Name string
	// Scope is the settings scope the apply resource detector keys on.
	Scope string
	// ConfigsAPI is the monitoring-configurations collection endpoint.
	ConfigsAPI string
	// ExtensionName is the data-acquisition extension backing this cloud.
	ExtensionName string
	// ResourceType is what apply's detector must return for a document of
	// this cloud.
	ResourceType apply.ResourceType
	// decode unmarshals a monitoring-configuration document into the cloud's
	// typed struct. The returned pointer is what a re-marshal reads back, so
	// this is the exact conversion `dtctl apply` performs on the document.
	decode func([]byte) (any, error)
}

// monitoringConfigClouds returns one spec per hyperscaler.
func monitoringConfigClouds() []cloudSpec {
	return []cloudSpec{
		{
			Name:          "aws",
			Scope:         awsmonitoringconfig.DefaultScope,
			ConfigsAPI:    awsmonitoringconfig.BaseAPI,
			ExtensionName: awsmonitoringconfig.ExtensionName,
			ResourceType:  apply.ResourceAWSMonitoringConfig,
			decode: func(b []byte) (any, error) {
				var c awsmonitoringconfig.AWSMonitoringConfig
				return &c, json.Unmarshal(b, &c)
			},
		},
		{
			Name:          "azure",
			Scope:         "integration-azure",
			ConfigsAPI:    azuremonitoringconfig.BaseAPI,
			ExtensionName: azuremonitoringconfig.ExtensionName,
			ResourceType:  apply.ResourceAzureMonitoringConfig,
			decode: func(b []byte) (any, error) {
				var c azuremonitoringconfig.AzureMonitoringConfig
				return &c, json.Unmarshal(b, &c)
			},
		},
		{
			Name:          "gcp",
			Scope:         "integration-gcp",
			ConfigsAPI:    gcpmonitoringconfig.BaseAPI,
			ExtensionName: gcpmonitoringconfig.ExtensionName,
			ResourceType:  apply.ResourceGCPMonitoringConfig,
			decode: func(b []byte) (any, error) {
				var c gcpmonitoringconfig.GCPMonitoringConfig
				return &c, json.Unmarshal(b, &c)
			},
		},
	}
}

// connectionSpec describes one hyperscaler's connection (credential) resource.
// All three are settings objects, so they share the settings endpoint and
// differ only by schema.
type connectionSpec struct {
	Name        string
	SchemaID    string
	SettingsAPI string
	decode      func([]byte) (any, error)
}

// connectionClouds returns one spec per hyperscaler connection schema.
func connectionClouds() []connectionSpec {
	return []connectionSpec{
		{
			Name:        "aws",
			SchemaID:    awsconnection.SchemaID,
			SettingsAPI: awsconnection.SettingsAPI,
			decode: func(b []byte) (any, error) {
				var c awsconnection.AWSConnection
				return &c, json.Unmarshal(b, &c)
			},
		},
		{
			Name:        "azure",
			SchemaID:    azureconnection.SchemaID,
			SettingsAPI: azureconnection.SettingsAPI,
			decode: func(b []byte) (any, error) {
				var c azureconnection.AzureConnection
				return &c, json.Unmarshal(b, &c)
			},
		},
		{
			Name:        "gcp",
			SchemaID:    gcpconnection.SchemaID,
			SettingsAPI: gcpconnection.SettingsAPI,
			decode: func(b []byte) (any, error) {
				var c gcpconnection.GCPConnection
				return &c, json.Unmarshal(b, &c)
			},
		},
	}
}

// rawGet fetches an endpoint and returns the response body verbatim. The
// fidelity tests need the document exactly as the server sent it, which the
// typed handlers cannot provide — their GetRaw re-marshals the typed struct
// and so has already lost whatever the struct does not model.
func rawGet(c *client.Client, endpoint string) ([]byte, error) {
	resp, err := c.HTTP().R().Get(endpoint)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("GET %s: status %d: %s", endpoint, resp.StatusCode(), resp.String())
	}
	return resp.Body(), nil
}

// rawObjectIDs fetches a collection endpoint and returns the objectId of every
// item, in server order.
func rawObjectIDs(c *client.Client, endpoint string) ([]string, error) {
	body, err := rawGet(c, endpoint)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			ObjectID string `json:"objectId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("GET %s returned unparseable JSON: %w", endpoint, err)
	}
	ids := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		if item.ObjectID != "" {
			ids = append(ids, item.ObjectID)
		}
	}
	return ids, nil
}

// docDiff is one field-level difference between a document as the server
// returned it and the same document re-marshalled from dtctl's typed struct.
type docDiff struct {
	// Path is a dotted field path. Indices inside arrays collapse to "[]" so
	// that a gap reported for one array element matches one knownFidelityGaps
	// entry rather than one per element.
	Path string
	// Kind is "dropped" (the key is gone) or "changed" (the value differs).
	Kind     string
	Server   string
	Rendered string
}

func (d docDiff) String() string {
	if d.Kind == "dropped" {
		return fmt.Sprintf("%s dropped (server had %s)", d.Path, d.Server)
	}
	return fmt.Sprintf("%s changed %s -> %s", d.Path, d.Server, d.Rendered)
}

// serverManagedFields are top-level envelope fields the API returns but dtctl
// deliberately does not model: they describe the stored object rather than the
// configuration it holds, they are read-only, and the API rejects nothing when
// they are absent from an update. Dropping them is correct, so the fidelity
// diff ignores them rather than every caller having to list them as a gap.
var serverManagedFields = map[string]bool{
	// Extension monitoring configurations.
	"modificationInfo": true,
	// Settings objects (the hyperscaler connections).
	"createdBy":       true,
	"owner":           true,
	"resourceContext": true,
	"searchSummary":   true,
	"updateToken":     true,
}

// diffDocument walks the server document against the re-marshalled one and
// splits the differences into two buckets.
//
//	real        fields the round trip genuinely loses or alters.
//	normalized  keys the round trip turns into an absent key because the
//	            server value was null or an empty collection. `omitempty` on
//	            the typed fields does this, and for these schemas an absent
//	            and an empty collection mean the same thing, so they are
//	            reported but not failed.
//
// An explicit `false` or `0` counts as real, not normalized: erasing a
// deliberate false is exactly the bug #442 had to fix for the nullable
// useIngestEnrichmentConfig / ingestEnrichmentMigrationProcessed pair.
func diffDocument(server, rendered []byte) (real, normalized []docDiff, err error) {
	var s, r map[string]any
	if err := json.Unmarshal(server, &s); err != nil {
		return nil, nil, fmt.Errorf("server document is not a JSON object: %w", err)
	}
	if err := json.Unmarshal(rendered, &r); err != nil {
		return nil, nil, fmt.Errorf("re-marshalled document is not a JSON object: %w", err)
	}
	walkDiff(s, r, "", &real, &normalized)
	return dedupeDiffs(real), dedupeDiffs(normalized), nil
}

func walkDiff(server, rendered map[string]any, path string, real, normalized *[]docDiff) {
	for _, key := range sortedKeys(server) {
		if path == "" && serverManagedFields[key] {
			continue
		}
		p := key
		if path != "" {
			p = path + "." + key
		}

		sv := server[key]
		rv, present := rendered[key]
		if !present {
			d := docDiff{Path: p, Kind: "dropped", Server: renderValue(sv)}
			if isEmptyJSONValue(sv) {
				*normalized = append(*normalized, d)
			} else {
				*real = append(*real, d)
			}
			continue
		}

		switch typed := sv.(type) {
		case map[string]any:
			if nested, ok := rv.(map[string]any); ok {
				walkDiff(typed, nested, p, real, normalized)
				continue
			}
		case []any:
			if nested, ok := rv.([]any); ok {
				if len(typed) != len(nested) {
					*real = append(*real, docDiff{
						Path:     p,
						Kind:     "changed",
						Server:   fmt.Sprintf("%d items", len(typed)),
						Rendered: fmt.Sprintf("%d items", len(nested)),
					})
					continue
				}
				for i := range typed {
					elemServer, okS := typed[i].(map[string]any)
					elemRendered, okR := nested[i].(map[string]any)
					if okS && okR {
						walkDiff(elemServer, elemRendered, p+"[]", real, normalized)
						continue
					}
					if renderValue(typed[i]) != renderValue(nested[i]) {
						*real = append(*real, docDiff{
							Path:     p + "[]",
							Kind:     "changed",
							Server:   renderValue(typed[i]),
							Rendered: renderValue(nested[i]),
						})
					}
				}
				continue
			}
		}

		if renderValue(sv) != renderValue(rv) {
			*real = append(*real, docDiff{Path: p, Kind: "changed", Server: renderValue(sv), Rendered: renderValue(rv)})
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isEmptyJSONValue reports whether v is null, an empty string, an empty array
// or an empty object. Booleans and numbers are never empty — see diffDocument.
func isEmptyJSONValue(v any) bool {
	switch typed := v.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
}

func renderValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	const max = 120
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

func dedupeDiffs(in []docDiff) []docDiff {
	seen := make(map[string]bool, len(in))
	out := make([]docDiff, 0, len(in))
	for _, d := range in {
		key := d.Kind + " " + d.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

// hasNonEmptyPath reports whether doc carries path with a value that
// isEmptyJSONValue would reject. Segments named "[]" descend into every array
// element and the answer is true if any element carries the rest of the path.
// The stale half of assertDocumentFidelity uses this so a knownFidelityGaps
// entry is only ever declared stale on a document that actually exercises it.
func hasNonEmptyPath(doc []byte, path string) bool {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return false
	}
	return lookupPath(root, strings.Split(path, "."))
}

func lookupPath(node any, segments []string) bool {
	if len(segments) == 0 {
		return !isEmptyJSONValue(node)
	}

	segment := segments[0]
	key := strings.TrimSuffix(segment, "[]")

	obj, ok := node.(map[string]any)
	if !ok {
		return false
	}
	child, ok := obj[key]
	if !ok {
		return false
	}

	if strings.HasSuffix(segment, "[]") {
		elems, ok := child.([]any)
		if !ok {
			return false
		}
		for _, elem := range elems {
			if lookupPath(elem, segments[1:]) {
				return true
			}
		}
		return false
	}
	return lookupPath(child, segments[1:])
}

// yamlThenBackToJSON mirrors `dtctl get <cloud> monitoring -o yaml` piped into
// `dtctl apply -f -`: the printer yaml-encodes the typed struct (yaml.v3 by
// reflection, so keys arrive all-lowercase and json tags are ignored) and
// apply runs the result through format.ValidateAndConvert before unmarshalling
// it again.
func yamlThenBackToJSON(v any) ([]byte, error) {
	encoded, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("yaml encode: %w", err)
	}
	converted, err := format.ValidateAndConvert(encoded)
	if err != nil {
		return nil, fmt.Errorf("apply could not read back the yaml dtctl printed: %w\n%s", err, encoded)
	}
	return converted, nil
}

// environmentRefusesCloudWrite reports whether err is the extension/tenant
// mismatch that makes hyperscaler monitoring-config writes impossible on an
// environment, rather than a dtctl defect.
//
// The da-* extension schemas advertise the central-enrichment properties (and
// their ownership validator) before every environment has the validator
// installed, so the tenant rejects any create or update against such a
// version — including a byte-identical no-op PUT. Creates also need a
// credential the cloud provider itself accepts: an AWS role ARN is validated
// by a real sts:AssumeRole, so a synthetic connection cannot back a
// configuration.
func environmentRefusesCloudWrite(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	for _, marker := range []string{
		"CENTRAL-ENRICHMENT-ACTIVATION-OWNERSHIP-VALIDATOR",
		"Invalid account ID provided",
		"Account ID must be unique",
		"Unsupported connection",
		"can not be assumed with externalId",
	} {
		if strings.Contains(msg, marker) {
			return marker, true
		}
	}
	return "", false
}

// skipUnlessCloudExtension skips the test when the data-acquisition extension
// for this cloud is absent from the environment, which makes every
// monitoring-configuration endpoint a 404.
func skipUnlessCloudExtension(t *testing.T, env *IntegrationEnv, spec cloudSpec) []string {
	t.Helper()

	ids, err := rawObjectIDs(env.Client, spec.ConfigsAPI)
	if err != nil {
		t.Skipf("skipping %s: monitoring configurations are not reachable in this environment (%s): %v",
			spec.Name, spec.ExtensionName, err)
	}
	if len(ids) == 0 {
		t.Skipf("skipping %s: environment has no %s monitoring configuration to round-trip",
			spec.Name, spec.Name)
	}
	return ids
}
