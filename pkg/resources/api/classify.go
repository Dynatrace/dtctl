package api

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/sdk/api/apispec"
)

// Classification is dtctl's verdict on what a generic request may do, and why.
//
// Nothing here is asserted by the caller. There is deliberately no flag that
// lets a caller declare the operation: a caller-asserted classification is a
// bypass, and the place the gate matters most is exactly where an agent is most
// likely to reach for the flag that makes the error go away.
type Classification struct {
	// SafetyOp is the operation the request is gated as.
	SafetyOp safety.Operation
	// Resolved reports whether a specification determined the access level. When
	// false, the gate is the strict fallback rather than a derived answer, and
	// the caller should say so.
	Resolved bool
	// Access is the specification-derived access level, or AccessUnknown.
	Access apispec.Access
	// Escalated reports that the verdict came from the curated destructive table
	// rather than from the method/specification floors. It is a *deliberate*
	// classification even when Resolved is false, so an error message must not
	// apologise for being unable to classify the request.
	Escalated bool
	// Scopes are the scopes the specification declares for the matched operation.
	// Empty means the specification declares none — which is common and is not
	// the same as "none required".
	Scopes []string
	// APIName and OperationID identify what the request path matched, when it
	// matched anything. They are what makes an error message actionable.
	APIName     string
	OperationID string
	// Reason explains the verdict in one sentence, for the error message and for
	// --dry-run.
	Reason string
}

// operationStrictness orders safety operations from most to least permissive, so
// two independent lower bounds can be combined by taking the stricter.
//
// Create sits below Update because readwrite-mine permits a create outright but
// refuses an update against unknown ownership.
var operationStrictness = map[safety.Operation]int{
	safety.OperationRead:         0,
	safety.OperationCreate:       1,
	safety.OperationUpdate:       2,
	safety.OperationDelete:       3,
	safety.OperationDeleteBucket: 4,
}

// stricter returns whichever of two operations gates more tightly.
func stricter(a, b safety.Operation) safety.Operation {
	if operationStrictness[b] > operationStrictness[a] {
		return b
	}
	return a
}

// methodFloor is the lower bound the HTTP method establishes on its own — what
// the request may do regardless of what any specification claims.
//
// POST's floor is Read, and that is the whole point: POST carries no information.
// Measured across 64 specified operations, 12 POSTs needed only a `:read` scope
// while one needed `:delete`, so a POST floor above Read would block reads (the
// Grail DQL query API is five-sixths POST and almost entirely non-mutating) and a
// POST floor of Create would permit that one delete as a create. The method
// contributes nothing here; the specification, or the strict fallback, decides.
func methodFloor(method string) safety.Operation {
	switch strings.ToUpper(method) {
	case "GET", "HEAD":
		return safety.OperationRead
	case "POST":
		return safety.OperationRead
	case "PUT", "PATCH":
		return safety.OperationUpdate
	case "DELETE":
		return safety.OperationDelete
	default:
		// An unrecognised method is unclassifiable, so it fails closed.
		return safety.OperationDelete
	}
}

// accessFloor is the lower bound the specification establishes.
//
// AccessUnknown resolves to Delete for anything but a conventional read: the
// strictest generally-reachable operation. Escalating rather than asking means
// existing safety semantics apply unchanged — with unknown ownership,
// OperationDelete proceeds only at readwrite-all — so no new safety level has to
// be invented for "might be anything".
func accessFloor(method string, access apispec.Access) safety.Operation {
	switch access {
	case apispec.AccessRead:
		return safety.OperationRead
	case apispec.AccessWrite:
		// The specification says "this writes"; the method says which kind. A POST
		// creates, a PUT or PATCH modifies something that already exists.
		if strings.EqualFold(method, "POST") {
			return safety.OperationCreate
		}
		return safety.OperationUpdate
	case apispec.AccessDelete:
		return safety.OperationDelete
	default:
		if apispec.MethodIsRead(method) {
			return safety.OperationRead
		}
		return safety.OperationDelete
	}
}

// Classify determines how a generic request must be gated.
//
// The verdict is the stricter of two independent lower bounds — what the method
// guarantees and what the specification declares — then escalated for paths whose
// destructiveness a URL cannot convey (see [escalateDestructive]). Taking the
// stricter of two floors, rather than reading one table, means neither source can
// weaken the other: a specification cannot turn a DELETE into a read, and a
// method cannot turn a declared delete into a create.
//
// spec may be nil, and op may be nil when no operation matched. Both are ordinary
// outcomes: an environment may not publish the specification, the path may belong
// to no listed API, or the API may declare nothing per operation. In every such
// case classification still returns a usable verdict rather than refusing to run.
func Classify(method, requestPath string, apiName string, op *Operation) Classification {
	method = strings.ToUpper(method)

	c := Classification{
		Access:  apispec.AccessUnknown,
		APIName: apiName,
	}

	if op != nil {
		c.OperationID = op.ID()
		c.Scopes = op.Scopes
		c.Access = op.Access()
	}
	c.Resolved = c.Access != apispec.AccessUnknown

	c.SafetyOp = stricter(methodFloor(method), accessFloor(method, c.Access))

	if escalated, why := escalateDestructive(method, requestPath, c.SafetyOp); why != "" {
		c.SafetyOp = escalated
		c.Reason = why
		c.Escalated = true
		return c
	}

	c.Reason = classificationReason(method, c)
	return c
}

// classificationReason renders the one-sentence explanation shown in errors and
// in --dry-run.
func classificationReason(method string, c Classification) string {
	switch {
	case c.Resolved:
		return fmt.Sprintf("%s declares scope %s, so this is gated as %s",
			c.OperationID, strings.Join(c.Scopes, ", "), c.SafetyOp)
	case apispec.MethodIsRead(method):
		return fmt.Sprintf("%s is a read by convention, so this is gated as %s", method, c.SafetyOp)
	case c.OperationID != "":
		return fmt.Sprintf(
			"%s declares no scope, so dtctl cannot tell what it does and gates it as %s",
			c.OperationID, c.SafetyOp)
	case c.APIName != "":
		// The path belongs to a known API, but no operation was matched — either its
		// specification could not be read or nothing in it covers this path. Saying
		// "no specification covers this path" here would be wrong twice: the API is
		// known, and it may well publish one.
		return fmt.Sprintf(
			"dtctl could not match this path to an operation in %s's specification, so it "+
				"cannot tell what %s does and gates it as %s", c.APIName, method, c.SafetyOp)
	default:
		return fmt.Sprintf(
			"no published specification covers this path, so dtctl cannot tell what %s does "+
				"and gates it as %s", method, c.SafetyOp)
	}
}

// destructivePattern escalates a request whose destructiveness the URL alone does
// not convey.
type destructivePattern struct {
	// methods the pattern applies to; empty means any.
	methods []string
	path    *regexp.Regexp
	op      safety.Operation
	why     string
}

// destructivePatterns is the one place a curated override is unavoidable.
//
// Everywhere else, classification errs strict on its own. Here it errs *lax*,
// because OperationDeleteBucket sits above OperationDelete — both readwrite-mine
// and readwrite-all refuse it, and only dangerously-unrestricted permits it — and
// nothing about a URL says "this destroys a data store". Without this table,
// deleting a bucket through the passthrough would pass at readwrite-all while
// `dtctl delete bucket` demands dangerously-unrestricted: the passthrough would be
// a strictly weaker gate than the command it shadows.
//
// The table is short today. The test that walks it is what keeps it honest as new
// irreversible endpoints appear.
var destructivePatterns = []destructivePattern{
	{
		// DELETE /platform/storage/management/v1/bucket-definitions/<name>
		// — the endpoint behind `dtctl delete bucket`.
		methods: []string{"DELETE"},
		path:    regexp.MustCompile(`^/platform/storage/management/v[0-9]+/bucket-definitions/[^/]+$`),
		op:      safety.OperationDeleteBucket,
		why: "deleting a bucket destroys all data in it, so it requires the same " +
			"dangerously-unrestricted context as 'dtctl delete bucket'",
	},
	{
		// POST /platform/storage/management/v1/bucket-definitions/<name>:truncate
		// — irreversible data loss without deleting the bucket itself. It is a
		// POST, so nothing but this table would raise it above a create.
		methods: []string{"POST"},
		path:    regexp.MustCompile(`^/platform/storage/management/v[0-9]+/bucket-definitions/[^/]+:truncate$`),
		op:      safety.OperationDeleteBucket,
		why: "truncating a bucket irreversibly destroys the records in it, so it " +
			"requires a dangerously-unrestricted context",
	},
	{
		// DELETE on Grail records — bulk record deletion.
		methods: []string{"POST", "DELETE"},
		path:    regexp.MustCompile(`^/platform/storage/management/v[0-9]+/record-deletion`),
		op:      safety.OperationDeleteBucket,
		why: "deleting stored records is irreversible, so it requires a " +
			"dangerously-unrestricted context",
	},
}

// escalateDestructive raises the gate for a curated set of irreversible
// endpoints. It returns the empty string when no pattern applies, and never
// lowers an operation.
//
// Patterns are matched against every routable spelling of the path, not just
// the literal one: the server-side router percent-decodes and normalizes
// before routing, so `foo%3Atruncate`, `x/../bucket-definitions/foo`, and
// `bucket-definitions//foo` all reach the endpoint the literal spelling would
// have named — and a table that matched only the literal spelling would gate
// them one level too low.
func escalateDestructive(method, requestPath string, current safety.Operation) (safety.Operation, string) {
	spellings := routableSpellings(requestPath)

	for _, p := range destructivePatterns {
		if !matchesMethod(p.methods, method) {
			continue
		}
		matched := false
		for _, s := range spellings {
			if p.path.MatchString(s) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		escalated := stricter(current, p.op)
		if escalated == current {
			return current, ""
		}
		return escalated, p.why
	}
	return current, ""
}

// routableSpellings returns every path a server-side router could reduce the
// request to: the spelling as given (query stripped), each successive
// percent-decoding, and the dot-segment/duplicate-slash-normalized form of
// each. A router decodes once; a misbehaving proxy chain can decode more than
// once, so decoding runs to a bounded fixpoint.
//
// Matching all of them is safe precisely because escalation only ever raises
// the gate: one spelling too many over-gates a bizarrely named resource, one
// too few reopens the bucket under-block through an encoded or dotted
// spelling.
func routableSpellings(requestPath string) []string {
	// Strip any query string: the classification is about the endpoint.
	if i := strings.IndexAny(requestPath, "?#"); i >= 0 {
		requestPath = requestPath[:i]
	}

	spellings := make([]string, 0, 8)
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			spellings = append(spellings, s)
		}
	}

	form := requestPath
	for range 4 {
		add(form)
		add(path.Clean(form))
		decoded, err := url.PathUnescape(form)
		if err != nil || decoded == form {
			break
		}
		form = decoded
	}
	add(form)
	add(path.Clean(form))
	return spellings
}

// CanonicalRequestPath returns the spelling of a request path that resolution
// and classification must run on, refusing spellings that change meaning under
// the normalization a server-side router performs before matching a route.
//
// The gate reasons about paths textually — the destructive table is a set of
// patterns, specification templates are segment patterns — while the platform
// router decodes and normalizes before routing. Any spelling a router reduces
// to a *different* path is therefore a way to make the text the gate sees
// disagree with the endpoint the server executes. Those spellings are refused
// outright: no legitimate caller writes `x/../y`, `a//b`, or an encoded `/`,
// and gating them "correctly" would mean guessing which router semantics the
// environment uses.
//
// Benign percent-encoding is different: `%20` in an identifier is the only way
// to spell a space, so it is accepted — and the *decoded* form is returned,
// because that is the path the server routes on. `%3A` and `:` must classify
// identically, or encoding a colon would spell `:truncate` past the
// destructive table.
func CanonicalRequestPath(requestPath string) (string, error) {
	p := requestPath
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must start with '/'", p)
	}

	decoded, err := url.PathUnescape(p)
	if err != nil {
		return "", fmt.Errorf("invalid percent-encoding in path %q: %v", p, err)
	}
	if strings.Count(decoded, "/") != strings.Count(p, "/") {
		return "", fmt.Errorf("path %q percent-encodes a '/': an encoded separator makes the routed "+
			"endpoint ambiguous, so dtctl refuses it — spell path separators literally", p)
	}
	for _, r := range decoded {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("path %q contains a control character after decoding; that is not a "+
				"spellable endpoint", p)
		}
	}
	// ';' is a path-parameter delimiter to servlet routers, '\' a separator to
	// others, and a decoded '?' or '#' is indistinguishable from a query or
	// fragment delimiter once the path is inspected as text. Each one makes the
	// endpoint the gate classifies diverge from the endpoint a router may
	// route, and none appears in a legitimate platform identifier.
	if i := strings.IndexAny(decoded, `;\?#`); i >= 0 {
		return "", fmt.Errorf("path %q contains %q, which routers treat as a delimiter: the routed "+
			"endpoint would be ambiguous, so dtctl refuses it", p, decoded[i])
	}
	if cleaned := path.Clean(decoded); cleaned != decoded {
		return "", fmt.Errorf("path %q normalizes to %q: dot segments and duplicate or trailing slashes "+
			"make the gate see a different endpoint than the server routes, so dtctl refuses the "+
			"spelling — write the path canonically", p, cleaned)
	}
	return decoded, nil
}

func matchesMethod(methods []string, method string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, m := range methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

// DestructivePatternCount reports how many curated escalations exist. The test
// that pins the table uses it to notice a silently emptied list.
func DestructivePatternCount() int { return len(destructivePatterns) }

// BlockedError explains a refusal in terms a caller can act on, naming the native
// command when one covers the path.
//
// "Denied" with no next step is what makes an agent start guessing, so the
// message always offers both routes: the native command (preferred) and the
// context change that would permit the request.
type BlockedError struct {
	Method      string
	RequestPath string
	Class       Classification
	// NativeCommand is the dtctl command covering this path as a caller would type
	// it, or "" when none does.
	NativeCommand string
	// Cause is the underlying safety refusal.
	Cause error
}

func (e *BlockedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cannot run %s %s — %s", e.Method, e.RequestPath, e.Class.Reason)

	if e.NativeCommand != "" {
		fmt.Fprintf(&b, "\n\n  Native command:  %s  (preferred)", e.NativeCommand)
	}
	// The note explains an *unresolved* verdict. An escalated one is deliberate,
	// and its reason already says exactly why — apologising for being unable to
	// classify it would be both wrong and an invitation to look for the override.
	if !e.Class.Resolved && !e.Class.Escalated {
		b.WriteString("\n\n  There is deliberately no flag to assert the operation: dtctl gates what " +
			"it cannot classify as the strictest reachable operation.")
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, "\n\n%s", e.Cause)
	}
	return b.String()
}

// Unwrap exposes the safety refusal, so errors.As still finds it. The agent
// envelope keeps its own case ahead of the generic safety one, because the
// classification — why this request counts as a delete — is the part that tells a
// caller what to do differently.
func (e *BlockedError) Unwrap() error { return e.Cause }

// Headline is the refusal in one line, without the appended explanations. The
// envelope carries those as suggestions instead.
func (e *BlockedError) Headline() string {
	return fmt.Sprintf("cannot run %s %s — %s", e.Method, e.RequestPath, e.Class.Reason)
}

// Suggestions are the routes out of the refusal: the native command when one
// covers the path, the operation's own documentation when the specification
// matched, and whatever the safety checker advised (usually the context change
// that would permit it).
func (e *BlockedError) Suggestions() []string {
	var s []string
	if e.NativeCommand != "" {
		s = append(s, fmt.Sprintf("%s  -- the native dtctl command for this API (preferred)", e.NativeCommand))
	}
	if e.Class.APIName != "" && e.Class.OperationID != "" {
		s = append(s, fmt.Sprintf("dtctl describe api %s --operation '%s'  -- what this operation does",
			QuoteArg(e.Class.APIName), e.Class.OperationID))
	}
	var safetyErr *safety.SafetyError
	if errors.As(e.Cause, &safetyErr) {
		s = append(s, safetyErr.Suggestions...)
	}
	if !e.Class.Resolved && !e.Class.Escalated {
		s = append(s, "there is no flag to assert the operation: dtctl gates a request it cannot "+
			"classify as the strictest reachable operation")
	}
	return s
}
