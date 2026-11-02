package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/sdk/api/apispec"
)

// op builds a matched operation with the given declared scopes. A nil scope slice
// models an operation whose specification declares nothing.
func op(method, path string, scopes ...string) *Operation {
	return &apispec.Operation{Method: method, Path: path, Scopes: scopes}
}

// TestClassify_SpecResolvedOperations covers the case the whole design turns on:
// the HTTP method does not predict mutation, so a declared scope must be able to
// move the verdict in both directions.
func TestClassify_SpecResolvedOperations(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		op     *Operation
		want   safety.Operation
		why    string
	}{
		{
			name:   "GET with a read scope",
			method: "GET",
			path:   "/platform/widget/v1/widgets",
			op:     op("GET", "/widgets", "widget:widgets:read"),
			want:   safety.OperationRead,
		},
		{
			name:   "read-shaped POST is a read",
			method: "POST",
			path:   "/platform/widget/v1/widgets/search",
			op:     op("POST", "/widgets/search", "widget:widgets:read"),
			want:   safety.OperationRead,
			why: "12 of 64 measured operations are POSTs needing only a :read scope; " +
				"method inference would block them in a readonly context",
		},
		{
			name:   "POST with a write scope is a create",
			method: "POST",
			path:   "/platform/widget/v1/widgets",
			op:     op("POST", "/widgets", "widget:widgets:write"),
			want:   safety.OperationCreate,
			why: "the specification says it writes and the method says it creates, " +
				"which readwrite-mine permits",
		},
		{
			name:   "POST with a delete scope is a delete",
			method: "POST",
			path:   "/platform/widget/v1/widgets:purge",
			op:     op("POST", "/widgets:purge", "widget:widgets:delete"),
			want:   safety.OperationDelete,
			why: "the dangerous direction: method inference calls this a create, which " +
				"readwrite-mine permits while it blocks deletes",
		},
		{
			name:   "PATCH with a write scope is an update",
			method: "PATCH",
			path:   "/platform/widget/v1/widgets/abc",
			op:     op("PATCH", "/widgets/{id}", "widget:widgets:write"),
			want:   safety.OperationUpdate,
		},
		{
			name:   "PUT with a read scope is still floored at update",
			method: "PUT",
			path:   "/platform/widget/v1/widgets/abc",
			op:     op("PUT", "/widgets/{id}", "widget:widgets:read"),
			want:   safety.OperationUpdate,
			why:    "a PUT is never a read, whatever the specification claims",
		},
		{
			name:   "DELETE with a write scope is still a delete",
			method: "DELETE",
			path:   "/platform/widget/v1/widgets/abc",
			op:     op("DELETE", "/widgets/{id}", "widget:widgets:write"),
			want:   safety.OperationDelete,
			why:    "the method is explicit; a weaker declared scope cannot lower it",
		},
		{
			name:   "DELETE with a delete scope",
			method: "DELETE",
			path:   "/platform/widget/v1/widgets/abc",
			op:     op("DELETE", "/widgets/{id}", "widget:widgets:delete"),
			want:   safety.OperationDelete,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Classify(tc.method, tc.path, "Widget Service", tc.op)
			require.Equal(t, tc.want, c.SafetyOp, tc.why)
			require.True(t, c.Resolved, "a declared scope must count as resolved")
			require.NotEmpty(t, c.Reason)
		})
	}
}

// TestClassify_UnresolvedFailsClosed pins the fallback. A substantial fraction of
// requests cannot be classified — two of six sampled APIs declare no
// per-operation scopes, one of them the largest — and every one of those is gated
// as the strictest generally-reachable operation rather than guessed at.
func TestClassify_UnresolvedFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		method string
		op     *Operation
		want   safety.Operation
		why    string
	}{
		{
			name:   "GET with no matched operation stays a read",
			method: "GET",
			want:   safety.OperationRead,
			why:    "GET is a read by convention, so an unknown GET is still permitted",
		},
		{
			name:   "HEAD with no matched operation stays a read",
			method: "HEAD",
			want:   safety.OperationRead,
		},
		{
			name:   "POST with no matched operation is a delete",
			method: "POST",
			want:   safety.OperationDelete,
			why:    "POST carries no information, so an unclassifiable POST assumes the worst",
		},
		{
			name:   "POST with an operation that declares no scope is a delete",
			method: "POST",
			op:     op("POST", "/workflows"),
			want:   safety.OperationDelete,
			why:    "an operation found but undeclared is no better than one not found",
		},
		{
			name:   "POST declaring an explicitly empty scope list is a delete",
			method: "POST",
			op:     op("POST", "/query:execute"),
			want:   safety.OperationDelete,
			why: "`ssoAuth: []` declares nothing; treating it as free access is the " +
				"single most dangerous misreading available here",
		},
		{
			name:   "PUT with no matched operation is a delete",
			method: "PUT",
			want:   safety.OperationDelete,
		},
		{
			name:   "PATCH with no matched operation is a delete",
			method: "PATCH",
			want:   safety.OperationDelete,
		},
		{
			name:   "DELETE with no matched operation is a delete",
			method: "DELETE",
			want:   safety.OperationDelete,
		},
		{
			name:   "an unrecognised scope verb is unresolved",
			method: "POST",
			op:     op("POST", "/things", "widget:widgets:teleport"),
			want:   safety.OperationDelete,
			why:    "a newly appearing verb must fall into the strict class, not default to read",
		},
		{
			name:   "an unknown method fails closed",
			method: "PROPFIND",
			want:   safety.OperationDelete,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Classify(tc.method, "/platform/example/v1/things", "", tc.op)
			require.Equal(t, tc.want, c.SafetyOp, tc.why)
			require.False(t, c.Resolved)
			require.NotEmpty(t, c.Reason, "an unresolved verdict must still explain itself")
		})
	}
}

// TestClassify_NoOperationIsEverLowered is the invariant behind taking the
// stricter of two floors: neither source can weaken the other. It is asserted
// exhaustively rather than by example, so a future edit to either floor cannot
// quietly introduce a case where a specification downgrades a method.
func TestClassify_NoOperationIsEverLowered(t *testing.T) {
	methods := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}
	scopeSets := [][]string{
		nil,
		{"widget:widgets:read"},
		{"widget:widgets:write"},
		{"widget:widgets:delete"},
		{"widget:widgets:teleport"},
	}

	for _, m := range methods {
		for _, scopes := range scopeSets {
			c := Classify(m, "/platform/example/v1/things", "", op(m, "/things", scopes...))
			require.GreaterOrEqual(t,
				operationStrictness[c.SafetyOp], operationStrictness[methodFloor(m)],
				"%s with scopes %v was gated below the floor its method guarantees", m, scopes)
		}
	}
}

// TestClassify_BucketPathsEscalate closes the one under-block the design found:
// OperationDeleteBucket sits *above* OperationDelete and needs
// dangerously-unrestricted, but nothing about a URL says "this destroys a data
// store". Without escalation the passthrough would be a weaker gate than the
// native command it shadows.
func TestClassify_BucketPathsEscalate(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   safety.Operation
	}{
		{
			name:   "deleting a bucket definition",
			method: "DELETE",
			path:   "/platform/storage/management/v1/bucket-definitions/my_bucket",
			want:   safety.OperationDeleteBucket,
		},
		{
			name:   "truncating a bucket",
			method: "POST",
			path:   "/platform/storage/management/v1/bucket-definitions/my_bucket:truncate",
			want:   safety.OperationDeleteBucket,
		},
		{
			name:   "deleting stored records",
			method: "POST",
			path:   "/platform/storage/management/v1/record-deletion",
			want:   safety.OperationDeleteBucket,
		},
		{
			name:   "listing bucket definitions is untouched",
			method: "GET",
			path:   "/platform/storage/management/v1/bucket-definitions",
			want:   safety.OperationRead,
		},
		{
			name:   "reading one bucket definition is untouched",
			method: "GET",
			path:   "/platform/storage/management/v1/bucket-definitions/my_bucket",
			want:   safety.OperationRead,
		},
		{
			name:   "updating a bucket definition is not a bucket deletion",
			method: "PATCH",
			path:   "/platform/storage/management/v1/bucket-definitions/my_bucket",
			want:   safety.OperationUpdate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Declared scopes are supplied so the verdict is otherwise *resolved*:
			// escalation has to fire even when classification succeeded, which is
			// the case the native command exposed.
			scope := "storage:bucket-definitions:read"
			switch tc.method {
			case "DELETE":
				scope = "storage:bucket-definitions:delete"
			case "POST", "PATCH":
				scope = "storage:bucket-definitions:write"
			}
			c := Classify(tc.method, tc.path, "Bucket Service", op(tc.method, "/bucket-definitions/{name}", scope))
			require.Equal(t, tc.want, c.SafetyOp)
		})
	}
}

// TestClassify_BucketEscalationMatchesNativeCommand pins the passthrough against
// the command it shadows. `dtctl delete bucket` gates on OperationDeleteBucket
// (cmd/get_buckets.go); the same request through the passthrough must not be
// easier to make.
func TestClassify_BucketEscalationMatchesNativeCommand(t *testing.T) {
	c := Classify("DELETE", "/platform/storage/management/v1/bucket-definitions/my_bucket",
		"Bucket Service", nil)

	require.Equal(t, safety.OperationDeleteBucket, c.SafetyOp,
		"the passthrough must never be a weaker gate than 'dtctl delete bucket'")
	require.Contains(t, c.Reason, "dangerously-unrestricted",
		"the reason must name what the caller actually needs")
}

// TestClassify_EscalationNeverLowers guards the escalation table itself: a
// pattern is an override that raises the gate, never one that relaxes it.
func TestClassify_EscalationNeverLowers(t *testing.T) {
	require.Positive(t, DestructivePatternCount(),
		"an emptied escalation table would silently reopen the bucket under-block")

	// A path matching a DeleteBucket pattern that somehow arrived already at
	// dangerously-unrestricted strictness must stay there.
	got, why := escalateDestructive("DELETE",
		"/platform/storage/management/v1/bucket-definitions/b", safety.OperationDeleteBucket)
	require.Equal(t, safety.OperationDeleteBucket, got)
	require.Empty(t, why, "no escalation to report when the gate is already at least as strict")
}

func TestClassify_QueryStringDoesNotDefeatEscalation(t *testing.T) {
	c := Classify("DELETE",
		"/platform/storage/management/v1/bucket-definitions/my_bucket?force=true", "", nil)
	require.Equal(t, safety.OperationDeleteBucket, c.SafetyOp,
		"a query string must not let a request slip past a path pattern")
}

// TestClassify_NormalizationDoesNotDefeatEscalation pins the escalation table
// against re-spellings a server-side router reduces to the destructive path: the
// router percent-decodes and normalizes before routing, so the request still
// truncates or deletes the bucket, and the gate must see through the spelling.
func TestClassify_NormalizationDoesNotDefeatEscalation(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "percent-encoded colon in a truncate call",
			method: "POST",
			path:   "/platform/storage/management/v1/bucket-definitions/foo%3Atruncate",
		},
		{
			name:   "double-encoded colon in a truncate call",
			method: "POST",
			path:   "/platform/storage/management/v1/bucket-definitions/foo%253Atruncate",
		},
		{
			name:   "dot segment routed back into a bucket deletion",
			method: "DELETE",
			path:   "/platform/storage/management/v1/x/../bucket-definitions/foo",
		},
		{
			name:   "duplicate slashes in a bucket deletion",
			method: "DELETE",
			path:   "/platform/storage/management//v1/bucket-definitions/foo",
		},
		{
			name:   "encoded dot segments in a bucket deletion",
			method: "DELETE",
			path:   "/platform/storage/management/v1/x/%2E%2E/bucket-definitions/foo",
		},
		{
			name:   "encoded record deletion",
			method: "POST",
			path:   "/platform/storage/management/v1/record%2Ddeletion",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Classify(tc.method, tc.path, "", nil)
			require.Equal(t, safety.OperationDeleteBucket, c.SafetyOp,
				"the spelling %q must not gate below the endpoint it routes to", tc.path)
		})
	}
}

// TestCanonicalRequestPath pins which spellings the passthrough accepts at all.
// Benign percent-encoding is legitimate (an identifier can contain a space or a
// colon) and canonicalizes to its decoded form; anything that changes structure
// under normalization is refused, because the gate would classify a different
// endpoint than the server routes.
func TestCanonicalRequestPath(t *testing.T) {
	accepted := []struct{ in, want string }{
		{"/platform/example/v1/things", "/platform/example/v1/things"},
		{"/platform/example/v1/things?filter=x%2Fy", "/platform/example/v1/things"},
		{"/platform/example/v1/things/a%20b", "/platform/example/v1/things/a b"},
		{"/platform/storage/management/v1/bucket-definitions/foo%3Atruncate",
			"/platform/storage/management/v1/bucket-definitions/foo:truncate"},
		{"/", "/"},
	}
	for _, tc := range accepted {
		got, err := CanonicalRequestPath(tc.in)
		require.NoError(t, err, "spelling %q must be accepted", tc.in)
		require.Equal(t, tc.want, got)
	}

	refused := []string{
		"/platform/example/v1/x/../things",       // dot segment
		"/platform/example/v1//things",           // duplicate slash
		"/platform/example/v1/things/",           // trailing slash
		"/platform/example/v1/things%2Fsub",      // encoded separator
		"/platform/example/v1/x/%2E%2E/things",   // encoded dot segment
		"/platform/example/v1/things%00",         // control character
		"/platform/example/v1/things;jsessionid", // path-parameter delimiter
		"/platform/example/v1/things%3Fq",        // decoded query delimiter
		"/platform/example/v1/things%zz",         // invalid encoding
		"platform/example/v1/things",             // not rooted
	}
	for _, in := range refused {
		_, err := CanonicalRequestPath(in)
		require.Error(t, err, "spelling %q must be refused", in)
	}
}

func TestClassify_CarriesScopesAndIdentity(t *testing.T) {
	c := Classify("POST", "/platform/widget/v1/widgets", "Widget Service",
		op("POST", "/widgets", "widget:widgets:write"))

	require.Equal(t, "Widget Service", c.APIName)
	require.Equal(t, "POST /widgets", c.OperationID)
	require.Equal(t, []string{"widget:widgets:write"}, c.Scopes)
	require.Equal(t, apispec.AccessWrite, c.Access)
}

func TestBlockedError_NamesNativeCommandAndRefusesToOfferAnEscape(t *testing.T) {
	c := Classify("POST", "/platform/automation/v1/workflows", "Automation", op("POST", "/workflows"))
	_, native := NativeCoverageForPath("/platform/automation/v1/workflows")
	err := &BlockedError{
		Method:        "POST",
		RequestPath:   "/platform/automation/v1/workflows",
		Class:         c,
		NativeCommand: native.Command,
	}

	msg := err.Error()
	require.Contains(t, msg, "dtctl get workflows",
		"a refusal without a next step is what makes an agent start guessing")
	require.Contains(t, msg, "no flag to assert the operation")
	require.NotContains(t, msg, "--op",
		"the message must not advertise an override that does not exist")
}
