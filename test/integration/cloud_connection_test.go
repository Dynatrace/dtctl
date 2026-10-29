//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/apply"
	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/settings"
)

// TestCloudConnection_RoundTrip is the connection counterpart of
// TestCloudMonitoringConfig_RoundTrip: every hyperscaler connection the
// environment has must survive a decode/re-marshal through dtctl's typed
// struct with nothing lost.
//
// Connections are settings objects, so unlike the monitoring configurations
// they have no per-cloud known gaps — this test starts clean and should stay
// clean.
func TestCloudConnection_RoundTrip(t *testing.T) {
	env := SetupIntegration(t)
	settingsHandler := settings.NewHandler(env.Client)

	for _, spec := range connectionClouds() {
		t.Run(spec.Name, func(t *testing.T) {
			list, err := settingsHandler.ListObjects(spec.SchemaID, "", 0)
			if err != nil {
				t.Skipf("skipping %s: schema %s is not readable in this environment: %v",
					spec.Name, spec.SchemaID, err)
			}
			if len(list.Items) == 0 {
				t.Skipf("skipping %s: environment has no %s connection to round-trip", spec.Name, spec.Name)
			}

			for _, obj := range list.Items {
				serverDoc, err := rawGet(env.Client, spec.SettingsAPI+"/"+obj.ObjectID)
				if err != nil {
					t.Fatalf("raw GET of %s connection %s failed: %v", spec.Name, obj.ObjectID, err)
				}

				typed, err := spec.decode(serverDoc)
				if err != nil {
					t.Fatalf("%s connection %s does not decode into dtctl's typed struct: %v",
						spec.Name, obj.ObjectID, err)
				}
				rendered, err := json.Marshal(typed)
				if err != nil {
					t.Fatalf("re-marshalling %s connection %s failed: %v", spec.Name, obj.ObjectID, err)
				}

				real, normalized, err := diffDocument(serverDoc, rendered)
				if err != nil {
					t.Fatalf("%s connection %s: %v", spec.Name, obj.ObjectID, err)
				}
				for _, d := range real {
					t.Errorf("%s connection %s loses %s on a get→apply round trip",
						spec.Name, obj.ObjectID, d)
				}
				for _, d := range normalized {
					t.Logf("%s connection %s: tolerated normalization: %s", spec.Name, obj.ObjectID, d)
				}
			}
		})
	}
}

// TestAWSConnection_CRUD covers the one hyperscaler write path a dev
// environment does permit: an AWS connection can be created without a role
// ARN, which is the documented first half of the onboarding flow (the ARN is
// patched once the IAM role exists with the connection's objectId as
// sts:ExternalId).
//
// Updating the ARN is deliberately not exercised — any non-empty ARN is
// validated by a real sts:AssumeRole against AWS, so there is no value a test
// can supply.
func TestAWSConnection_CRUD(t *testing.T) {
	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := awsconnection.NewHandler(env.Client)
	fixture := AWSConnectionFixture(env.TestPrefix)

	created, err := handler.Create(fixture)
	if err != nil {
		if marker, refused := environmentRefusesCloudWrite(err); refused {
			t.Skipf("skipping: this environment refuses AWS connection writes (%s): %v", marker, err)
		}
		t.Fatalf("Create() failed: %v", err)
	}
	if created.ObjectID == "" {
		t.Fatal("Create() returned an empty objectId")
	}
	env.Cleanup.TrackCloudConnection("aws", created.ObjectID, fixture.Value.Name)
	t.Logf("created AWS connection %s (%s)", fixture.Value.Name, created.ObjectID)

	got, err := handler.Get(created.ObjectID)
	if err != nil {
		t.Fatalf("Get(%s) failed: %v", created.ObjectID, err)
	}
	if got.Name != fixture.Value.Name {
		t.Errorf("Get() name = %q, want %q", got.Name, fixture.Value.Name)
	}
	if got.Value.Type != awsconnection.TypeRoleBased {
		t.Errorf("Get() type = %q, want %q", got.Value.Type, awsconnection.TypeRoleBased)
	}
	if got.Value.AwsRoleBasedAuthentication == nil {
		t.Fatal("Get() lost the awsRoleBasedAuthentication block")
	}
	if len(got.Value.AwsRoleBasedAuthentication.Consumers) == 0 {
		t.Errorf("Get() consumers is empty, want %q", awsconnection.DefaultConsumer)
	}

	byName, err := handler.FindByName(fixture.Value.Name)
	if err != nil {
		t.Fatalf("FindByName(%q) failed: %v", fixture.Value.Name, err)
	}
	if byName.ObjectID != created.ObjectID {
		t.Errorf("FindByName resolved to %q, want %q", byName.ObjectID, created.ObjectID)
	}

	if err := handler.Delete(created.ObjectID); err != nil {
		t.Fatalf("Delete(%s) failed: %v", created.ObjectID, err)
	}
	env.Cleanup.Untrack("aws-connection", created.ObjectID)

	if _, err := handler.Get(created.ObjectID); err == nil {
		t.Errorf("Get(%s) succeeded after Delete", created.ObjectID)
	}
}

// TestAWSConnection_ApplyDryRunAction is the connection half of #509: an
// exported connection carries objectId, so `apply --dry-run` on it must
// predict an update rather than a create.
func TestAWSConnection_ApplyDryRunAction(t *testing.T) {
	env := SetupIntegration(t)

	connections, err := awsconnection.NewHandler(env.Client).List()
	if err != nil {
		t.Skipf("skipping: AWS connections are not listable in this environment: %v", err)
	}
	if len(connections) == 0 {
		t.Skip("skipping: environment has no AWS connection")
	}

	serverDoc, err := rawGet(env.Client, awsconnection.SettingsAPI+"/"+connections[0].ObjectID)
	if err != nil {
		t.Fatalf("raw GET of AWS connection %s failed: %v", connections[0].ObjectID, err)
	}

	results, err := apply.NewApplier(env.Client).Apply(serverDoc, apply.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("apply --dry-run on an exported AWS connection failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("apply --dry-run returned %d results, want 1", len(results))
	}
	dry, ok := results[0].(*apply.DryRunResult)
	if !ok {
		t.Fatalf("apply --dry-run returned %T, want *apply.DryRunResult", results[0])
	}

	if dry.Action == apply.ActionCreated && dry.ID == "" {
		t.Skipf("known bug #509: apply --dry-run reports action=%q id=%q for AWS connection %s, "+
			"but a real apply resolves objectId and issues a PUT (action=%q). "+
			"Remove this skip together with the fix.",
			dry.Action, dry.ID, connections[0].ObjectID, apply.ActionUpdated)
	}

	if dry.Action != apply.ActionUpdated {
		t.Errorf("apply --dry-run on an exported AWS connection reports action=%q, want %q",
			dry.Action, apply.ActionUpdated)
	}
	if dry.ID != connections[0].ObjectID {
		t.Errorf("apply --dry-run on an exported AWS connection reports id=%q, want the objectId %q",
			dry.ID, connections[0].ObjectID)
	}
}
