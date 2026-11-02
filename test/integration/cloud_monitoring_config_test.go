//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/apply"
	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/awsmonitoringconfig"
)

// knownFidelityGaps records, per cloud, the document paths that live
// monitoring configurations carry but dtctl's typed structs do not model, so a
// `get -o yaml` → `apply -f` round trip drops them. Every entry is a field the
// da-* activation schema defines and the Go struct has no counterpart for;
// none of them are fields dtctl chooses to discard.
//
// This list is CLOSED in both directions:
//
//   - A dropped or altered field that is not listed fails the test. That is
//     the guard the issue asks for — the next schema addition that nobody
//     models shows up here rather than as a silent wipe on someone's tenant.
//   - A listed field that survives the round trip on a document which
//     actually carries it also fails the test, with an instruction to delete
//     the entry. A fix therefore cannot leave a stale excuse behind.
//
// It is empty: every typed struct in a monitoring configuration's value tree
// now keeps the members it does not model (pkg/util/unknownfields), and the
// booleans whose explicit false matters are pointers, so nothing the server
// sends is lost on a round trip (#516, #608). tenantInstanceId in particular
// has modificationPolicy NEVER and is nullable in all three schemas, so an
// update that omitted it would itself modify a never-modifiable property — the
// shape that made useIngestEnrichmentConfig a hard rejection in #442.
//
// Add an entry only for a field dtctl deliberately discards, with the reason.
var knownFidelityGaps = map[string][]string{}

func isKnownFidelityGap(cloud, path string) bool {
	for _, known := range knownFidelityGaps[cloud] {
		if known == path {
			return true
		}
	}
	return false
}

// TestCloudMonitoringConfig_RoundTrip is the dropped-field guard for
// aws/azure/gcp monitoring configurations: it takes every configuration the
// environment has, decodes it with the same typed struct `dtctl apply` uses
// and re-marshals it, then asserts nothing the server sent went missing.
//
// It is read-only on purpose. A create/update round trip is not reachable on a
// dev environment (see TestCloudMonitoringConfig_ApplyRoundTrip), but the
// failure mode the issue cares about — a field the typed struct does not model
// silently disappearing from an update payload — is fully observable without
// writing anything.
func TestCloudMonitoringConfig_RoundTrip(t *testing.T) {
	env := SetupIntegration(t)

	for _, spec := range monitoringConfigClouds() {
		t.Run(spec.Name, func(t *testing.T) {
			ids := skipUnlessCloudExtension(t, env, spec)

			for _, id := range ids {
				serverDoc, err := rawGet(env.Client, spec.ConfigsAPI+"/"+id)
				if err != nil {
					t.Fatalf("raw GET of %s config %s failed: %v", spec.Name, id, err)
				}

				typed, err := spec.decode(serverDoc)
				if err != nil {
					t.Fatalf("%s config %s does not decode into dtctl's typed struct: %v", spec.Name, id, err)
				}
				rendered, err := json.Marshal(typed)
				if err != nil {
					t.Fatalf("re-marshalling %s config %s failed: %v", spec.Name, id, err)
				}

				assertDocumentFidelity(t, spec.Name, id, serverDoc, rendered)
			}
		})
	}
}

// assertDocumentFidelity enforces knownFidelityGaps in both directions for one
// document.
func assertDocumentFidelity(t *testing.T, cloud, id string, serverDoc, rendered []byte) {
	t.Helper()

	real, normalized, err := diffDocument(serverDoc, rendered)
	if err != nil {
		t.Fatalf("%s %s: %v", cloud, id, err)
	}

	reproduced := make(map[string]bool, len(real))
	for _, d := range real {
		reproduced[d.Path] = true
		if isKnownFidelityGap(cloud, d.Path) {
			t.Logf("%s %s: known gap: %s", cloud, id, d)
			continue
		}
		t.Errorf("%s config %s loses %s on a get→apply round trip.\n"+
			"Either model the field in the typed struct, or — if dropping it is "+
			"correct — add %q to knownFidelityGaps[%q] with the reason.",
			cloud, id, d, d.Path, cloud)
	}

	// The other direction: a gap that no longer reproduces on a document that
	// still carries the field means the struct was fixed and the entry is now
	// a stale excuse.
	for _, known := range knownFidelityGaps[cloud] {
		if reproduced[known] || !hasNonEmptyPath(serverDoc, known) {
			continue
		}
		t.Errorf("%s config %s round-trips %q intact, but it is still listed in "+
			"knownFidelityGaps[%q] — delete the entry.", cloud, id, known, cloud)
	}

	for _, d := range normalized {
		t.Logf("%s %s: tolerated normalization: %s", cloud, id, d)
	}
}

// TestCloudMonitoringConfig_YAMLRoundTrip pins the second half of the
// `get -o yaml` → `apply -f` path: that apply can read back the YAML dtctl
// prints without losing anything *relative to the typed struct*.
//
// This is a separate risk from the dropped fields above. The printer
// yaml-encodes the structs by reflection, so json tags are ignored and every
// modelled key arrives lowercased (`deploymentregion`, `activationcontext`);
// apply converts that YAML to JSON and unmarshals it with encoding/json, which
// matches field names case-insensitively. Reflection never sees the Extra map
// that holds unmodelled members, so each type in the value tree has a
// MarshalYAML that appends them under their API names (unknownfields.YAML); a
// type that lost it would drop them on this path while JSON stayed intact.
func TestCloudMonitoringConfig_YAMLRoundTrip(t *testing.T) {
	env := SetupIntegration(t)

	for _, spec := range monitoringConfigClouds() {
		t.Run(spec.Name, func(t *testing.T) {
			ids := skipUnlessCloudExtension(t, env, spec)

			for _, id := range ids {
				serverDoc, err := rawGet(env.Client, spec.ConfigsAPI+"/"+id)
				if err != nil {
					t.Fatalf("raw GET of %s config %s failed: %v", spec.Name, id, err)
				}

				typed, err := spec.decode(serverDoc)
				if err != nil {
					t.Fatalf("%s config %s does not decode: %v", spec.Name, id, err)
				}
				beforeYAML, err := json.Marshal(typed)
				if err != nil {
					t.Fatalf("marshalling %s config %s failed: %v", spec.Name, id, err)
				}

				viaYAML, err := yamlThenBackToJSON(typed)
				if err != nil {
					t.Fatalf("%s config %s: %v", spec.Name, id, err)
				}

				reparsed, err := spec.decode(viaYAML)
				if err != nil {
					t.Fatalf("%s config %s does not decode after the yaml round trip: %v", spec.Name, id, err)
				}
				afterYAML, err := json.Marshal(reparsed)
				if err != nil {
					t.Fatalf("re-marshalling %s config %s failed: %v", spec.Name, id, err)
				}

				real, _, err := diffDocument(beforeYAML, afterYAML)
				if err != nil {
					t.Fatalf("%s config %s: %v", spec.Name, id, err)
				}
				for _, d := range real {
					t.Errorf("%s config %s: `get -o yaml` output does not survive apply's yaml→json conversion: %s",
						spec.Name, id, d)
				}
			}
		})
	}
}

// TestCloudMonitoringConfig_ApplyDryRunAction pins that `apply --dry-run`
// predicts the same action a real apply would take, which for an exported
// configuration is always an update: the cloud apply paths resolve the target
// by objectId (and by value.description as a fallback) and issue a PUT.
//
// Blocked on #509 — dry-run only reads objectId for settings resources, so the
// cloud types fall through to a doc["id"] lookup they never carry and every
// dry-run claims "created" with an empty id. The skip below stops firing the
// moment #509 lands, at which point the assertions underneath take over.
func TestCloudMonitoringConfig_ApplyDryRunAction(t *testing.T) {
	env := SetupIntegration(t)
	applier := apply.NewApplier(env.Client)

	for _, spec := range monitoringConfigClouds() {
		t.Run(spec.Name, func(t *testing.T) {
			ids := skipUnlessCloudExtension(t, env, spec)

			serverDoc, err := rawGet(env.Client, spec.ConfigsAPI+"/"+ids[0])
			if err != nil {
				t.Fatalf("raw GET of %s config %s failed: %v", spec.Name, ids[0], err)
			}

			results, err := applier.Apply(serverDoc, apply.ApplyOptions{DryRun: true})
			if err != nil {
				t.Fatalf("apply --dry-run on an exported %s config failed: %v", spec.Name, err)
			}
			if len(results) != 1 {
				t.Fatalf("apply --dry-run returned %d results, want 1", len(results))
			}

			dry, ok := results[0].(*apply.DryRunResult)
			if !ok {
				t.Fatalf("apply --dry-run returned %T, want *apply.DryRunResult", results[0])
			}

			if got := apply.ResourceType(dry.ResourceType); got != spec.ResourceType {
				t.Errorf("apply detected resource type %q for an exported %s config, want %q",
					got, spec.Name, spec.ResourceType)
			}

			if dry.Action == apply.ActionCreated && dry.ID == "" {
				t.Skipf("known bug #509: apply --dry-run reports action=%q id=%q for %s config %s, "+
					"but a real apply resolves objectId and issues a PUT (action=%q). "+
					"Remove this skip together with the fix.",
					dry.Action, dry.ID, spec.Name, ids[0], apply.ActionUpdated)
			}

			if dry.Action != apply.ActionUpdated {
				t.Errorf("apply --dry-run on an exported %s config reports action=%q, want %q",
					spec.Name, dry.Action, apply.ActionUpdated)
			}
			if dry.ID != ids[0] {
				t.Errorf("apply --dry-run on an exported %s config reports id=%q, want the document's objectId %q",
					spec.Name, dry.ID, ids[0])
			}
		})
	}
}

// TestCloudMonitoringConfig_ApplyRoundTrip is the mutating round trip: create a
// configuration, export it, apply the export back and assert the stored
// document is unchanged.
//
// It skips on every environment reachable today. The da-* schema versions
// advertise the central-enrichment ownership validator before the tenant has
// it, so both dev environments reject any create or update against those
// versions with a 400 that has nothing to do with the payload — a
// byte-identical no-op PUT of an existing AWS configuration is refused too.
// An AWS credential the extension accepts also needs a role ARN that a real
// sts:AssumeRole succeeds against, which no synthetic fixture can produce.
//
// The test is written out rather than left as a TODO so that it runs the day
// an environment does allow it; TestCloudMonitoringConfig_RoundTrip covers the
// dropped-field failure mode in the meantime.
func TestCloudMonitoringConfig_ApplyRoundTrip(t *testing.T) {
	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := awsmonitoringconfig.NewHandler(env.Client)

	version, err := handler.GetLatestVersion()
	if err != nil {
		if errors.Is(err, awsmonitoringconfig.ErrExtensionNotInEnvironment) {
			t.Skipf("skipping: %s is not added to this environment", awsmonitoringconfig.ExtensionName)
		}
		t.Fatalf("GetLatestVersion() failed: %v", err)
	}

	credential, err := anyAWSCredential(env)
	if err != nil {
		t.Skipf("skipping: %v", err)
	}

	fixture := AWSMonitoringConfigFixture(env.TestPrefix, version, credential)
	created, err := handler.Create(ToJSONBytes(t, fixture))
	if err != nil {
		if marker, refused := environmentRefusesCloudWrite(err); refused {
			t.Skipf("skipping: this environment refuses AWS monitoring-config writes (%s): %v", marker, err)
		}
		t.Fatalf("Create() failed: %v", err)
	}
	env.Cleanup.TrackCloudMonitoringConfig("aws", created.ObjectID, fixture.Value.Description)

	exported, err := rawGet(env.Client, awsmonitoringconfig.BaseAPI+"/"+created.ObjectID)
	if err != nil {
		t.Fatalf("raw GET of the created config failed: %v", err)
	}

	// Go through YAML the way `dtctl get ... -o yaml > f && dtctl apply -f f`
	// does, so the assertion covers the printer and the reader too.
	typed, err := monitoringConfigClouds()[0].decode(exported)
	if err != nil {
		t.Fatalf("created config does not decode: %v", err)
	}
	roundTripped, err := yamlThenBackToJSON(typed)
	if err != nil {
		t.Fatalf("%v", err)
	}

	applier := apply.NewApplier(env.Client)
	results, err := applier.Apply(roundTripped, apply.ApplyOptions{})
	if err != nil {
		if marker, refused := environmentRefusesCloudWrite(err); refused {
			t.Skipf("skipping: this environment refuses AWS monitoring-config updates (%s): %v", marker, err)
		}
		t.Fatalf("apply of the exported config failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("apply returned %d results, want 1", len(results))
	}

	reread, err := rawGet(env.Client, awsmonitoringconfig.BaseAPI+"/"+created.ObjectID)
	if err != nil {
		t.Fatalf("raw GET after apply failed: %v", err)
	}

	real, normalized, err := diffDocument(exported, reread)
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, d := range real {
		t.Errorf("applying an unmodified export changed the stored config: %s", d)
	}
	for _, d := range normalized {
		t.Logf("tolerated normalization after apply: %s", d)
	}
}

// anyAWSCredential returns a credential built from an AWS connection that
// already exists in the environment. A monitoring configuration cannot be
// created without one, and dtctl cannot manufacture a usable connection: the
// role ARN is validated against AWS itself, so a connection created without
// one has no account id and the extension answers "Invalid account ID
// provided".
func anyAWSCredential(env *IntegrationEnv) (awsmonitoringconfig.Credential, error) {
	connections, err := awsconnection.NewHandler(env.Client).List()
	if err != nil {
		return awsmonitoringconfig.Credential{}, fmt.Errorf("AWS connections are not listable here: %w", err)
	}

	for _, conn := range connections {
		accountID := awsmonitoringconfig.AccountIDFromRoleArn(conn.RoleArn)
		if accountID == "" {
			continue
		}
		return awsmonitoringconfig.Credential{
			Enabled:      false,
			Description:  conn.Name,
			ConnectionID: conn.ObjectID,
			AccountID:    accountID,
		}, nil
	}
	return awsmonitoringconfig.Credential{},
		errors.New("environment has no AWS connection with a role ARN, so no monitoring config can be created")
}
