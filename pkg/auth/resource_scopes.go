package auth

import (
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// Access is the access level a command verb needs against a resource. It is
// derived from the verb and its safety operation (see AccessForVerb), not
// hand-listed per command.
type Access string

const (
	AccessRead   Access = "read"
	AccessWrite  Access = "write"
	AccessDelete Access = "delete"
	AccessRun    Access = "run"
	AccessClaim  Access = "claim"
)

// AccessScopes lists the OAuth/IAM scopes required for each access level of a
// single resource type. A nil/empty list means the access level either does not
// apply to the resource or has no distinct scope. Delete falls back to Write
// when Delete is empty, matching resources that fold deletion into their write
// scope (e.g. workflows, buckets) rather than exposing a distinct :delete scope
// (e.g. documents).
//
// The per-level lists are conjunctive: a token needs every scope in them.
// Alternatives covers the endpoints that document "one of the following scopes
// is required": each entry is another complete scope set the platform accepts in
// place of that level's list. The list stays the one dtctl requests at login and
// reports as required; an alternative only lets a scope check accept a token
// that holds it instead.
type AccessScopes struct {
	Read   []string `json:"read,omitempty" yaml:"read,omitempty"`
	Write  []string `json:"write,omitempty" yaml:"write,omitempty"`
	Delete []string `json:"delete,omitempty" yaml:"delete,omitempty"`
	Run    []string `json:"run,omitempty" yaml:"run,omitempty"`
	Claim  []string `json:"claim,omitempty" yaml:"claim,omitempty"`
	// Alternatives maps an access level to the scope sets accepted instead of
	// its list. Any one fully granted set satisfies the level.
	Alternatives map[Access][][]string `json:"alternatives,omitempty" yaml:"alternatives,omitempty"`
}

// For returns the scopes required for the given access level, applying the
// delete→write fallback for resources without a distinct delete scope.
func (a AccessScopes) For(access Access) []string {
	switch access {
	case AccessRead:
		return a.Read
	case AccessWrite:
		return a.Write
	case AccessDelete:
		if len(a.Delete) > 0 {
			return a.Delete
		}
		return a.Write
	case AccessRun:
		return a.Run
	case AccessClaim:
		return a.Claim
	default:
		return nil
	}
}

// AlternativesFor returns the scope sets accepted in place of For(access),
// applying the same delete→write fallback so an alternative always refers to the
// list For returned.
func (a AccessScopes) AlternativesFor(access Access) [][]string {
	if access == AccessDelete && len(a.Delete) == 0 {
		access = AccessWrite
	}
	return a.Alternatives[access]
}

// platformManagementRead is the requirement of the Platform Management endpoints
// (/platform/management/v1/...), which accept any one of app-engine:apps:run,
// app-engine:functions:run and platform-management:environments:read.
//
// app-engine:apps:run is the one dtctl lists and requests: the narrow
// platform-management:environments:read is not grantable to OAuth clients via
// the standard login flow, and apps:run already is at every safety level (via
// the "app" resource), so no extra login scope is needed. The other two are
// declared as alternatives so that --check-scopes accepts a token holding only
// one of them, for which the call succeeds.
var platformManagementRead = AccessScopes{
	Read: []string{"app-engine:apps:run"},
	Alternatives: map[Access][][]string{
		AccessRead: {{"app-engine:functions:run"}, {"platform-management:environments:read"}},
	},
}

// ResourceScopes is the single source of truth for which scope each
// (resource, access) pair requires. Keys are the canonical *singular* resource
// name as it appears in the `dtctl commands` catalog (plural names resolve via
// ScopesForResource). GetScopesForSafetyLevel is composed from this table plus
// the non-resource scope groups below, so the per-command scopes and the
// per-safety-level OAuth union cannot diverge.
//
// Scope strings mirror docs/TOKEN_SCOPES.md ("Quick Reference by Resource
// Type") and were verified against each resource's API endpoint (the path the
// SDK/resource handler calls); inline comments cite the endpoint where the
// mapping is non-obvious.
var ResourceScopes = map[string]AccessScopes{
	// Automation
	"workflow":           {Read: []string{"automation:workflows:read"}, Write: []string{"automation:workflows:write"}, Run: []string{"automation:workflows:run"}},
	"workflow-execution": {Read: []string{"automation:workflows:read"}},
	"wfe-task-result":    {Read: []string{"automation:workflows:read"}},
	// Delete requires write (the DELETE endpoint) plus read (the pre-delete GET
	// for ownership resolution). No separate delete scope exists in the API.
	"scheduling-rule": {Read: []string{"automation:rules:read"}, Write: []string{"automation:rules:write"}, Delete: []string{"automation:rules:write", "automation:rules:read"}},

	// Documents (dashboards / notebooks are Documents). Delete is a distinct
	// scope (documents move to trash on delete).
	"document":  {Read: []string{"document:documents:read"}, Write: []string{"document:documents:write"}, Delete: []string{"document:documents:delete"}},
	"dashboard": {Read: []string{"document:documents:read"}, Write: []string{"document:documents:write"}, Delete: []string{"document:documents:delete"}},
	"notebook":  {Read: []string{"document:documents:read"}, Write: []string{"document:documents:write"}, Delete: []string{"document:documents:delete"}},
	"trash":     {Read: []string{"document:trash.documents:read"}, Write: []string{"document:trash.documents:restore"}, Delete: []string{"document:trash.documents:delete"}},

	// Claiming has its own scope and access level: it changes only the caller's
	// own access to a document, unlike the owner-side :write.
	"environment-share": {Claim: []string{"document:environment-shares:claim"}},

	// `inventory arrivals` is pure DQL against the environment's own data —
	// same Grail read surface as `query`, no managed resource of its own.
	"arrivals": {Read: QueryScopes},

	// `repo-scope discover` reads aggregates of spans and logs. When those
	// find nothing it searches the entity tables too, and a token that cannot
	// read them gets a note instead of an error, so storage:entities:read is
	// not required.
	"discover": {Read: []string{"storage:spans:read", "storage:logs:read"}},

	// Grail storage. Buckets are managed via the bucket data scopes (delete
	// folds into write); lookups and segments are stored as files / filter
	// segments respectively.
	"bucket":  {Read: []string{"storage:buckets:read"}, Write: []string{"storage:buckets:write"}},
	"lookup":  {Read: []string{"storage:files:read"}, Write: []string{"storage:files:write"}, Delete: []string{"storage:files:delete"}},
	"segment": {Read: []string{"storage:filter-segments:read"}, Write: []string{"storage:filter-segments:write"}, Delete: []string{"storage:filter-segments:delete"}},

	// Settings
	"setting":         {Read: []string{"settings:objects:read", "app-settings:objects:read"}, Write: []string{"settings:objects:write"}},
	"settings-schema": {Read: []string{"settings:schemas:read"}},

	// SLOs
	"slo":          {Read: []string{"slo:slos:read", "slo:objective-templates:read"}, Write: []string{"slo:slos:write"}},
	"slo-template": {Read: []string{"slo:objective-templates:read"}},

	// Extensions
	"extension":        {Read: []string{"extensions:definitions:read"}, Write: []string{"extensions:definitions:write"}},
	"extension-config": {Read: []string{"extensions:configurations:read"}, Write: []string{"extensions:configurations:write"}},

	// App Engine
	"app":         {Read: []string{"app-engine:apps:run"}, Write: []string{"app-engine:apps:install"}, Delete: []string{"app-engine:apps:delete"}},
	"function":    {Read: []string{"app-engine:apps:run"}, Run: []string{"app-engine:functions:run"}},
	"edgeconnect": {Read: []string{"app-engine:edge-connects:read"}, Write: []string{"app-engine:edge-connects:write"}, Delete: []string{"app-engine:edge-connects:delete"}},
	// intents are listed/launched via the App Engine app registry
	// (/platform/app-engine/registry/v1/apps).
	"intent": {Read: []string{"app-engine:apps:run"}},
	// runtime SDK metadata from the function executor
	// (/platform/app-engine/function-executor/v1/sdk-versions).
	"sdk-version": {Read: []string{"app-engine:apps:run"}},

	// Davis AI
	"analyzer": {Read: []string{"davis:analyzers:read"}, Run: []string{"davis:analyzers:execute"}},
	"copilot":  {Run: []string{"davis-copilot:conversations:execute"}},
	// lists Davis CoPilot skills (/platform/davis/copilot/v1/skills)
	"copilot-skill": {Read: []string{"davis-copilot:conversations:execute"}},

	// Notifications (/platform/notification/v2/...)
	"notification": {Read: []string{"notification:notifications:read"}, Write: []string{"notification:notifications:write"}},

	// IAM (read-only surfaces in dtctl)
	"user":  {Read: []string{"iam:users:read"}},
	"group": {Read: []string{"iam:groups:read"}},

	// Live Debugger (single scope covers all operations including snapshot queries)
	"breakpoint": {Read: []string{"dev-obs:breakpoints:set"}, Write: []string{"dev-obs:breakpoints:set"}, Delete: []string{"dev-obs:breakpoints:set"}},
	"snapshot":   {Read: []string{"dev-obs:breakpoints:set"}},

	// Hub catalog (/platform/hub/v1/catalog/...). dtctl's login requests
	// hub:catalog:read for this surface (docs/TOKEN_SCOPES.md also lists
	// extensions:definitions:read; the Hub catalog endpoint is distinct from the
	// Extensions API).
	"hub-extension":         {Read: []string{"hub:catalog:read"}},
	"hub-extension-release": {Read: []string{"hub:catalog:read"}},

	// Davis anomaly detectors are Settings objects
	// (schema builtin:davis.anomaly-detectors via the Settings API).
	"anomaly-detector": {Read: []string{"settings:objects:read"}, Write: []string{"settings:objects:write"}},

	// OpenPipeline. The classic-pipelines translation endpoint
	// (/platform/openpipeline/v1/classic-pipelines/translate) is a read-only
	// call that returns the translated pipeline document.
	"classic-pipelines": {Read: []string{"settings:objects:read"}},

	// LQL-to-DQL matcher translation
	// (/platform/openpipeline/v1/matcher/lqlToDql) is a read-only stateless
	// call that converts a single LQL matcher expression into its DQL equivalent.
	"lql-to-dql": {Read: []string{"openpipeline:configurations:read"}},

	// OpenPipeline verify and preview endpoints — all read-only; they validate
	// or preview pipeline definitions without persisting any changes.
	"openpipeline-matcher":       {Read: []string{"openpipeline:configurations:read"}},
	"openpipeline-dql-processor": {Read: []string{"openpipeline:configurations:read"}},
	"preview-processor":          {Read: []string{"openpipeline:configurations:read"}},

	// Cloud monitoring (enable/create aws|azure|gcp) touches two APIs: the
	// hyperscaler-authentication *connection* (Settings API,
	// builtin:hyperscaler-authentication.connections.*) and the *monitoring
	// configuration* (Extensions monitoring-configurations API). Both scope
	// families are required.
	"aws":   {Read: []string{"settings:objects:read", "extensions:configurations:read"}, Write: []string{"settings:objects:write", "extensions:configurations:write"}},
	"azure": {Read: []string{"settings:objects:read", "extensions:configurations:read"}, Write: []string{"settings:objects:write", "extensions:configurations:write"}},
	"gcp":   {Read: []string{"settings:objects:read", "extensions:configurations:read"}, Write: []string{"settings:objects:write", "extensions:configurations:write"}},

	// API specifications — the environment's own API index and each API's
	// OpenAPI document. Reading them needs a valid token but no resource scope:
	// they are the same documents the environment serves to its Swagger UI.
	//
	// The Run level is deliberately empty rather than guessed. `exec api` calls
	// an arbitrary operation, so the scope it needs is whatever that operation's
	// specification declares — resolved per call (see pkg/resources/api), never
	// from a static table. An entry here claiming a single scope for every
	// possible target would be wrong for almost all of them.
	"api": {},

	// Platform Management (/platform/management/v1/...): any one of three
	// scopes authorizes these endpoints — see platformManagementRead.
	"environment":      platformManagementRead,
	"license":          platformManagementRead,
	"license-settings": platformManagementRead,
}

// localResources are catalog subcommands that operate entirely on the local
// machine (config, contexts, credentials, skills, aliases) and therefore
// require no platform scopes. They are excluded from the scope catalog and the
// build-guard test in pkg/commands.
var localResources = map[string]bool{
	// config / context management
	"current-context": true, "delete-context": true, "describe-context": true,
	"get-contexts": true, "use-context": true, "set-context": true,
	"set-credentials": true, "delete-credentials": true,
	"migrate-tokens": true, "init": true,
	"view": true, "current": true, "set": true,
	// development-feature opt-in listing (reads the binary's own registry)
	"list-development": true,
	// ctx aliases
	"describe": true, "delete": true, "token": true, "discover-account": true,
	// auth (local token storage / introspection)
	"login": true, "logout": true, "refresh": true, "status": true, "whoami": true,
	// alias management
	"export": true, "import": true, "list": true, "create": true,
	// skills (local install)
	"install": true, "uninstall": true,
}

// QueryScopes are the Grail read scopes required by DQL (`query`, `verify`,
// `wait`). They attach to those verbs rather than to a managed resource.
var QueryScopes = []string{
	"storage:logs:read",
	"storage:events:read",
	"storage:metrics:read",
	"storage:spans:read",
	"storage:bizevents:read",
	"storage:entities:read",
	"storage:smartscape:read",
	"storage:system:read",
	"storage:security.events:read",
}

// singularize returns a basic singular form by stripping a trailing "s".
// Mirrors pkg/commands.singularize; kept here to avoid an import cycle.
func singularize(name string) string {
	if strings.HasSuffix(name, "s") {
		return strings.TrimSuffix(name, "s")
	}
	return name
}

// ScopesForResource returns the scopes required for the given access level of a
// resource. The resource name may be singular or plural ("workflow" or
// "workflows"); aliases must be resolved by the caller. Returns nil when the
// resource has no scope mapping (e.g. a local-only command).
func ScopesForResource(resource string, access Access) []string {
	if s, ok := lookupResourceScopes(resource); ok {
		return s.For(access)
	}
	return nil
}

// AlternativeScopesForResource returns the scope sets the platform accepts in
// place of ScopesForResource(resource, access), or nil when that requirement has
// no alternative. Names resolve as in ScopesForResource.
func AlternativeScopesForResource(resource string, access Access) [][]string {
	if s, ok := lookupResourceScopes(resource); ok {
		return s.AlternativesFor(access)
	}
	return nil
}

// lookupResourceScopes finds a resource's entry by its singular or plural name.
func lookupResourceScopes(resource string) (AccessScopes, bool) {
	if s, ok := ResourceScopes[resource]; ok {
		return s, true
	}
	s, ok := ResourceScopes[singularize(resource)]
	return s, ok
}

// HasResourceScopes reports whether the resource has an entry in the canonical
// table (regardless of whether any access level is populated).
func HasResourceScopes(resource string) bool {
	_, ok := lookupResourceScopes(resource)
	return ok
}

// IsLocalResource reports whether the named subcommand operates only on the
// local machine and needs no platform scopes.
func IsLocalResource(name string) bool {
	return localResources[name]
}

// DocumentAdminScope lets a Document API request act as the effective owner of
// every document (`get documents|dashboards|notebooks --admin-access`, which
// sends admin-access=true). The API refuses such a request with 403
// "Insufficient permissions to request admin-access" unless the token carries
// this scope *and* the tenant's IAM policy grants the permission of the same
// name — two separate gates.
//
// It is not tied to a resource access level, because no plain document
// operation needs it: the flag alone does (see flagScopeRequirements in cmd/).
// Login requests it from readwrite-all upward. That covers the administrative
// levels the flag is meant for, and it is harmless for a user whose policy
// lacks the permission: the OAuth client is allowed to request the scope, the
// token merely carries it, and without the IAM permission the API still says
// no. readonly and readwrite-mine leave it out on purpose — acting as the owner
// of other people's documents is exactly what those levels exist to rule out.
const DocumentAdminScope = "document:documents:admin"

// grailExtendedReadScopes are additional Grail read scopes granted at the
// readonly, readwrite-all and dangerously-unrestricted levels. readwrite-mine
// intentionally omits them (it focuses on write access to one's own resources).
var grailExtendedReadScopes = []string{
	"storage:application.snapshots:read",
	"storage:user.events:read",
	"storage:user.sessions:read",
	"storage:user.replays:read",
	"storage:fieldsets:read",
	"storage:fieldset-definitions:read",
}

// scopeSet collects scopes preserving first-seen order and de-duplicating.
type scopeSet struct {
	seen map[string]bool
	list []string
}

func newScopeSet() *scopeSet { return &scopeSet{seen: map[string]bool{}} }

func (s *scopeSet) add(scopes ...string) {
	for _, sc := range scopes {
		if sc == "" || s.seen[sc] {
			continue
		}
		s.seen[sc] = true
		s.list = append(s.list, sc)
	}
}

// addResource adds the scopes for the given access levels of a resource, sourced
// from the canonical ResourceScopes table.
func (s *scopeSet) addResource(resource string, accesses ...Access) {
	for _, a := range accesses {
		s.add(ScopesForResource(resource, a)...)
	}
}

// addReadTier adds the read-access scopes common to readonly/readwrite-mine/
// readwrite-all. When extended is true, the additional Grail read scopes are
// included (readwrite-mine passes false).
func (s *scopeSet) addReadTier(extended bool) {
	s.addResource("workflow", AccessRead)
	s.addResource("scheduling-rule", AccessRead)
	s.addResource("dashboard", AccessRead)
	s.add("document:direct-shares:read", "document:trash.documents:read")
	s.addResource("slo", AccessRead)
	s.addResource("setting", AccessRead)
	s.addResource("settings-schema", AccessRead)
	s.addResource("extension", AccessRead)
	s.addResource("extension-config", AccessRead)
	s.add(QueryScopes...)
	if extended {
		s.add(grailExtendedReadScopes...)
	}
	s.addResource("bucket", AccessRead)
	s.add("storage:bucket-definitions:read")
	s.addResource("lookup", AccessRead)
	s.addResource("segment", AccessRead)
	s.addResource("user", AccessRead)
	s.addResource("group", AccessRead)
	s.addResource("analyzer", AccessRead)
	s.addResource("app", AccessRead)
	s.addResource("edgeconnect", AccessRead)
	s.addResource("notification", AccessRead)
	s.add("hub:catalog:read")
	s.addResource("classic-pipelines", AccessRead)
	s.addResource("lql-to-dql", AccessRead)
	s.addResource("openpipeline-matcher", AccessRead)
	s.addResource("openpipeline-dql-processor", AccessRead)
	s.addResource("preview-processor", AccessRead)
}

// addMineWrites adds the write/run scopes granted from readwrite-mine upward:
// modifying one's own resources, running workflows/functions/analyzers.
func (s *scopeSet) addMineWrites() {
	s.addResource("dashboard", AccessWrite, AccessDelete)
	s.add("document:direct-shares:write", "document:direct-shares:delete", "document:trash.documents:restore")
	s.addResource("workflow", AccessWrite, AccessRun)
	s.addResource("scheduling-rule", AccessWrite)
	s.addResource("slo", AccessWrite)
	s.addResource("setting", AccessWrite)
	s.addResource("extension", AccessWrite)
	s.addResource("extension-config", AccessWrite)
	s.addResource("lookup", AccessWrite)
	s.addResource("segment", AccessWrite)
	s.add("iam:service-users:use")
	s.addResource("analyzer", AccessRun)
	s.add("davis-copilot:conversations:execute")
	s.addResource("function", AccessRun)
	s.addResource("environment-share", AccessClaim)
	s.add("email:emails:send", "dev-obs:breakpoints:set")
}

// addAllExtras adds the scopes that readwrite-all grants on top of
// readwrite-mine: environment sharing, Grail writes, bucket writes, CoPilot
// generation, and app/EdgeConnect lifecycle.
func (s *scopeSet) addAllExtras() {
	s.add("document:environment-shares:read", "document:environment-shares:write")
	s.add(DocumentAdminScope)
	s.add("storage:logs:write", "storage:events:write", "storage:metrics:write")
	s.addResource("bucket", AccessWrite)
	s.add("davis-copilot:nl2dql:execute", "davis-copilot:dql2nl:execute", "davis-copilot:document-search:execute")
	s.addResource("app", AccessWrite, AccessDelete)
	s.addResource("edgeconnect", AccessWrite)
	s.addResource("notification", AccessWrite)
}

// addUnrestricted builds the dangerously-unrestricted scope set, which is not a
// strict superset of readwrite-all: it grants environment shares instead of
// direct shares and adds destructive Grail/storage scopes.
func (s *scopeSet) addUnrestricted() {
	// reads (environment shares replace direct shares)
	s.addResource("workflow", AccessRead)
	s.addResource("scheduling-rule", AccessRead)
	s.addResource("dashboard", AccessRead)
	s.add("document:environment-shares:read", "document:trash.documents:read")
	s.addResource("slo", AccessRead)
	s.addResource("setting", AccessRead)
	s.addResource("settings-schema", AccessRead)
	s.addResource("extension", AccessRead)
	s.addResource("extension-config", AccessRead)
	s.add(QueryScopes...)
	s.add(grailExtendedReadScopes...)
	s.addResource("bucket", AccessRead)
	s.add("storage:bucket-definitions:read")
	s.addResource("lookup", AccessRead)
	s.addResource("segment", AccessRead)
	s.addResource("user", AccessRead)
	s.addResource("group", AccessRead)
	s.addResource("analyzer", AccessRead)
	s.addResource("app", AccessRead)
	s.addResource("edgeconnect", AccessRead)
	s.addResource("notification", AccessRead)
	s.add("hub:catalog:read")
	s.addResource("classic-pipelines", AccessRead)
	s.addResource("lql-to-dql", AccessRead)
	s.addResource("openpipeline-matcher", AccessRead)
	s.addResource("openpipeline-dql-processor", AccessRead)
	s.addResource("preview-processor", AccessRead)
	// writes / destructive
	s.addResource("dashboard", AccessWrite, AccessDelete)
	s.add("document:environment-shares:write")
	s.addResource("environment-share", AccessClaim)
	s.add(DocumentAdminScope)
	s.add("document:trash.documents:restore", "document:trash.documents:delete")
	s.addResource("workflow", AccessWrite, AccessRun)
	s.addResource("scheduling-rule", AccessWrite)
	s.addResource("slo", AccessWrite)
	s.addResource("setting", AccessWrite)
	s.addResource("extension", AccessWrite)
	s.addResource("extension-config", AccessWrite)
	s.add("storage:logs:write", "storage:events:write", "storage:metrics:write")
	s.addResource("bucket", AccessWrite)
	s.add("storage:bucket-definitions:write", "storage:bucket-definitions:delete", "storage:bucket-definitions:truncate")
	s.add("storage:fieldset-definitions:write")
	s.addResource("lookup", AccessWrite, AccessDelete)
	s.addResource("segment", AccessWrite)
	s.add("storage:filter-segments:share", "storage:filter-segments:delete", "storage:filter-segments:admin")
	s.add("storage:records:delete")
	s.add("iam:service-users:use")
	s.addResource("analyzer", AccessRun)
	s.add("davis-copilot:conversations:execute", "davis-copilot:nl2dql:execute", "davis-copilot:dql2nl:execute", "davis-copilot:document-search:execute")
	s.addResource("app", AccessWrite, AccessDelete)
	s.addResource("function", AccessRun)
	s.addResource("edgeconnect", AccessWrite, AccessDelete)
	s.addResource("notification", AccessWrite)
	s.add("email:emails:send", "dev-obs:breakpoints:set")
}

// safetyLevelScopes composes the OAuth scope union for a safety level from the
// canonical ResourceScopes table plus the non-resource scope groups above. It
// backs GetScopesForSafetyLevel; the TestGetScopesForSafetyLevelBaseline test
// pins its output so the composition cannot drift from the documented sets.
func safetyLevelScopes(level config.SafetyLevel) []string {
	if level == "" {
		level = config.DefaultSafetyLevel
	}
	s := newScopeSet()
	s.add("openid", "offline_access")

	switch level {
	case config.SafetyLevelReadOnly:
		s.addReadTier(true)
	case config.SafetyLevelReadWriteMine:
		s.addReadTier(false)
		s.addMineWrites()
	case config.SafetyLevelReadWriteAll:
		s.addReadTier(true)
		s.addMineWrites()
		s.addAllExtras()
	case config.SafetyLevelDangerouslyUnrestricted:
		s.addUnrestricted()
	default:
		return safetyLevelScopes(config.SafetyLevelReadWriteAll)
	}
	return s.list
}

// AccessForVerb derives the access level a verb needs from its name and safety
// operation. exec maps to run; OperationDelete maps to delete; any other
// mutating operation maps to write; everything else is read. safetyOp is the
// MutatingVerbs value (e.g. "OperationDelete") or "" for read-only verbs.
func AccessForVerb(verb, safetyOp string) Access {
	switch {
	case verb == "exec":
		return AccessRun
	case verb == "claim":
		return AccessClaim
	case safetyOp == "OperationDelete":
		return AccessDelete
	case safetyOp != "":
		return AccessWrite
	default:
		return AccessRead
	}
}
