package cmd

import (
	"context"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// commandTree bundles the fresh root with the references executeTree needs
// directly (get, for installGetListPaging).
type commandTree struct {
	root *cobra.Command
	get  *cobra.Command
}

// newCommandTree builds a complete, freshly allocated dtctl command tree for
// one concurrent invocation (RunOptions.Concurrent). The root persistent flags,
// --dry-run and get's --limit/--fields bind to storage on the invocation ctx
// carries, and every command-specific flag binds to a constructor-local
// variable, so nothing one request parses is shared with another. Storage is
// passed to the constructors rather than looked up, because a command being
// built has no context yet to find the invocation through.
//
// Each constructor carries its command's whole wiring — flags, required
// marks, hooks, stability tiers — so this tree is the same surface as the
// init()-wired singleton the CLI runs. TestNewCommandTreeMatchesSingleton
// holds the two together; wiring added to an init() instead of a constructor
// fails it.
//
// The returned tree deliberately omits:
//   - Shell-completion registrations (registerFlagCompletion): cobra keeps
//     them in a process-wide map that is never pruned, so registering them
//     per request would retain every request's tree for the process lifetime.
//   - Development-tier commands (their developmentCommand entries reference
//     the singleton parent pointers and cannot be grafted onto a fresh tree;
//     the concurrent engine blocks those commands via RunOptions.BlockedCommands
//     anyway).
//
// See docs/dev/CONCURRENT_EXECUTION.md.
func newCommandTree(ictx context.Context) *commandTree {
	root := &cobra.Command{
		Use:           "dtctl",
		Short:         "Dynatrace platform CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// cobra's global OnInitialize hook cannot serve a concurrent
			// tree (see initConfigHook), so its request-scoped half runs here.
			ctx := cmdContext(cmd)
			initConfig(ctx)
			if inv := current(ctx); inv != nil {
				inv.initDone = true
			}
			if err := rejectUnimplementedDryRun(cmd); err != nil {
				return err
			}
			return validateGlobalFlags(ctx)
		},
		Long: `dtctl is a kubectl-inspired CLI tool for managing Dynatrace platform resources.

It provides a consistent interface for interacting with workflows, documents,
SLOs, queries, and other Dynatrace platform capabilities.`,
	}
	inv := concurrentInvocation(ictx)
	if inv == nil {
		// Binding to the package globals instead would also rebind viper and
		// --context to this tree: a programming error, not a fallback.
		panic("cmd: newCommandTree called outside a concurrent invocation")
	}
	registerRootPersistentFlags(root, &inv.flags)
	// Hidden --dry-run on the root catches the flag at parse time for commands
	// without their own dry-run, giving a better error than "unknown flag".
	// The singleton tree gets this from dryrun.go's init(); fresh trees need it here.
	root.PersistentFlags().Bool("dry-run", false, "print what would be done without doing it")
	_ = root.PersistentFlags().MarkHidden("dry-run")

	// --- get ---
	get := newGetCmd(&inv.getListLimit, &inv.getListFields)
	get.AddCommand(
		newGetWorkflowsCmd(),
		newGetSchedulingRulesCmd(),
		newGetWorkflowExecutionsCmd(),
		newGetWfeTaskResultCmd(),
		newGetDashboardsCmd(),
		newGetNotebooksCmd(),
		newGetTrashCmd(),
		newGetSLOsCmd(),
		newGetSLOTemplatesCmd(),
		newGetNotificationsCmd(),
		newGetBucketsCmd(),
		newGetLookupsCmd(),
		newGetAppsCmd(),
		newGetFunctionsCmd(),
		newGetIntentsCmd(),
		newGetEdgeConnectsCmd(),
		newGetUsersCmd(),
		newGetGroupsCmd(),
		newGetSDKVersionsCmd(),
		newGetAnalyzersCmd(),
		newGetCopilotSkillsCmd(),
		newGetSettingsSchemasCmd(),
		newGetSettingsCmd(),
	)
	get.AddCommand(newGetBreakpointsCmd())
	get.AddCommand(newGetSnapshotsCmd())
	get.AddCommand(
		newGetExtensionsCmd(),
		newGetExtensionConfigsCmd(),
		newGetDocumentsCmd(),
		newGetSegmentsCmd(),
		newGetAnomalyDetectorsCmd(),
		newGetHubExtensionsCmd(),
		newGetHubExtensionReleasesCmd(),
		newGetAPIsCmd(),
		newGetEnvironmentCmd(),
		newGetLicenseCmd(),
		newGetLicenseSettingsCmd(),
	)
	getAzure := newGetAzureProviderCmd()
	getAzure.AddCommand(
		newGetAzureConnectionCmd(),
		newGetAzureMonitoringConfigCmd(),
		newGetAzureMonitoringConfigLocationsCmd(),
		newGetAzureMonitoringConfigFeatureSetsCmd(),
	)
	get.AddCommand(getAzure)
	getAWS := newGetAWSProviderCmd()
	getAWS.AddCommand(
		newGetAWSConnectionCmd(),
		newGetAWSMonitoringConfigCmd(),
		newGetAWSMonitoringConfigRegionsCmd(),
		newGetAWSMonitoringConfigFeatureSetsCmd(),
	)
	get.AddCommand(getAWS)
	getGCP := newGetGCPProviderCmd()
	getGCPConn := newGetGCPConnectionCmd()
	getGCPConn.AddCommand(newGetGCPConnectionPrincipalCmd())
	getGCP.AddCommand(
		getGCPConn,
		newGetGCPMonitoringConfigCmd(),
		newGetGCPMonitoringConfigLocationsCmd(),
		newGetGCPMonitoringConfigFeatureSetsCmd(),
	)
	get.AddCommand(getGCP)

	// --- create ---
	create := newCreateCmd()
	create.AddCommand(
		newCreateWorkflowCmd(),
		newCreateSchedulingRuleCmd(),
		newCreateNotebookCmd(),
		newCreateDashboardCmd(),
		newCreateDocumentCmd(),
		newCreateSettingsCmd(),
		newCreateSLOCmd(),
		newCreateBucketCmd(),
		newCreateLookupCmd(),
		newCreateEdgeConnectCmd(),
	)
	create.AddCommand(newCreateBreakpointCmd())
	create.AddCommand(
		newCreateSegmentCmd(),
		newCreateAnomalyDetectorCmd(),
		newCreateExtensionCmd(),
	)
	createAzure := newCreateAzureProviderCmd()
	createAzure.AddCommand(newCreateAzureConnectionCmd(), newCreateAzureMonitoringConfigCmd())
	create.AddCommand(createAzure)
	createAWS := newCreateAWSProviderCmd()
	createAWS.AddCommand(newCreateAWSConnectionCmd(), newCreateAWSMonitoringConfigCmd())
	create.AddCommand(createAWS)
	createGCP := newCreateGCPProviderCmd()
	createGCP.AddCommand(newCreateGCPConnectionCmd(), newCreateGCPMonitoringConfigCmd())
	create.AddCommand(createGCP)

	// --- update ---
	update := newUpdateCmd()
	update.AddCommand(newUpdateDocumentCmd(), newUpdateSettingsHintCmd())
	update.AddCommand(newUpdateBreakpointCmd())
	updateAzure := newUpdateAzureProviderCmd()
	updateAzure.AddCommand(newUpdateAzureConnectionCmd(), newUpdateAzureMonitoringConfigCmd())
	update.AddCommand(updateAzure)
	updateAWS := newUpdateAWSProviderCmd()
	updateAWS.AddCommand(newUpdateAWSConnectionCmd(), newUpdateAWSMonitoringConfigCmd())
	update.AddCommand(updateAWS)
	updateGCP := newUpdateGCPProviderCmd()
	updateGCP.AddCommand(newUpdateGCPConnectionCmd(), newUpdateGCPMonitoringConfigCmd())
	update.AddCommand(updateGCP)
	update.AddCommand(newUpdateExtensionCmd(), newUpdateExtensionsCmd())

	// --- delete ---
	del := newDeleteCmd()
	del.AddCommand(
		newDeleteWorkflowCmd(),
		newDeleteSchedulingRuleCmd(),
		newDeleteDashboardCmd(),
		newDeleteNotebookCmd(),
		newDeleteTrashCmd(),
		newDeleteSLOCmd(),
		newDeleteNotificationCmd(),
		newDeleteBucketCmd(),
		newDeleteLookupCmd(),
		newDeleteSettingsCmd(),
		newDeleteAppCmd(),
		newDeleteEdgeConnectCmd(),
		newDeleteDocumentCmd(),
		newDeleteSegmentCmd(),
		newDeleteAnomalyDetectorCmd(),
	)
	del.AddCommand(newDeleteBreakpointCmd())
	deleteAzure := newDeleteAzureProviderCmd()
	deleteAzure.AddCommand(newDeleteAzureConnectionCmd(), newDeleteAzureMonitoringConfigCmd())
	del.AddCommand(deleteAzure)
	deleteAWS := newDeleteAWSProviderCmd()
	deleteAWS.AddCommand(newDeleteAWSConnectionCmd(), newDeleteAWSMonitoringConfigCmd())
	del.AddCommand(deleteAWS)
	deleteGCP := newDeleteGCPProviderCmd()
	deleteGCP.AddCommand(newDeleteGCPConnectionCmd(), newDeleteGCPMonitoringConfigCmd())
	del.AddCommand(deleteGCP)

	// --- describe ---
	describe := newDescribeCmd()
	describe.AddCommand(
		newDescribeWorkflowCmd(),
		newDescribeSchedulingRuleCmd(),
	)
	describe.AddCommand(newDescribeBreakpointCmd())
	describe.AddCommand(
		newDescribeWorkflowExecutionCmd(),
		newDescribeDashboardCmd(),
		newDescribeNotebookCmd(),
		newDescribeTrashCmd(),
		newDescribeBucketCmd(),
		newDescribeLookupCmd(),
		newDescribeAppCmd(),
		newDescribeFunctionCmd(),
		newDescribeIntentCmd(),
		newDescribeEdgeConnectCmd(),
		newDescribeUserCmd(),
		newDescribeGroupCmd(),
		newDescribeSettingsCmd(),
		newDescribeSettingsSchemaCmd(),
		newDescribeSLOCmd(),
		newDescribeExtensionCmd(),
		newDescribeExtensionConfigCmd(),
		newDescribeDocumentCmd(),
		newDescribeSegmentCmd(),
		newDescribeAnomalyDetectorCmd(),
		newDescribeHubExtensionCmd(),
		newDescribeAnalyzerCmd(),
		newDescribeAPICmd(),
		newDescribeEnvironmentCmd(),
		newDescribeLicenseCmd(),
	)
	describeAzure := newDescribeAzureProviderCmd()
	describeAzure.AddCommand(newDescribeAzureConnectionCmd(), newDescribeAzureMonitoringConfigCmd())
	describe.AddCommand(describeAzure)
	describeAWS := newDescribeAWSProviderCmd()
	describeAWS.AddCommand(newDescribeAWSConnectionCmd(), newDescribeAWSMonitoringConfigCmd())
	describe.AddCommand(describeAWS)
	describeGCP := newDescribeGCPProviderCmd()
	describeGCP.AddCommand(newDescribeGCPConnectionCmd(), newDescribeGCPMonitoringConfigCmd())
	describe.AddCommand(describeGCP)

	// --- edit ---
	edit := newEditCmd()
	edit.AddCommand(
		newEditWorkflowCmd(),
		newEditDashboardCmd(),
		newEditNotebookCmd(),
		newEditDocumentCmd(),
		newEditSettingCmd(),
		newEditSegmentCmd(),
		newEditAnomalyDetectorCmd(),
	)
	editAWS := newEditAWSProviderCmd()
	editAWS.AddCommand(newEditAWSMonitoringCmd())
	edit.AddCommand(editAWS)
	editAzure := newEditAzureProviderCmd()
	editAzure.AddCommand(newEditAzureMonitoringCmd())
	edit.AddCommand(editAzure)
	editGCP := newEditGCPProviderCmd()
	editGCP.AddCommand(newEditGCPMonitoringCmd())
	edit.AddCommand(editGCP)

	// --- exec ---
	exec := newExecCmd()
	execCopilot := newExecCopilotCmd()
	execCopilot.AddCommand(newExecCopilotNl2DqlCmd(), newExecCopilotDql2NlCmd(), newExecCopilotDocSearchCmd())
	exec.AddCommand(
		newExecDQLCmd(),
		newExecWorkflowCmd(),
		newExecFunctionCmd(),
		newExecAnalyzerCmd(),
		execCopilot,
		newExecSLOCmd(),
		newExecPreviewProcessorCmd(),
		newExecAPICmd(),
	)

	// --- enable ---
	enable := newEnableCmd()
	enableAWS := newEnableAWSProviderCmd()
	enableAWS.AddCommand(newEnableAWSMonitoringCmd())
	enable.AddCommand(enableAWS)
	enableAzure := newEnableAzureProviderCmd()
	enableAzure.AddCommand(newEnableAzureMonitoringCmd())
	enable.AddCommand(enableAzure)
	enableGCP := newEnableGCPProviderCmd()
	enableGCP.AddCommand(newEnableGCPMonitoringCmd())
	enable.AddCommand(enableGCP)

	// --- disable ---
	disable := newDisableCmd()
	disableAWS := newDisableAWSProviderCmd()
	disableAWS.AddCommand(newDisableAWSMonitoringCmd())
	disable.AddCommand(disableAWS)
	disableAzure := newDisableAzureProviderCmd()
	disableAzure.AddCommand(newDisableAzureMonitoringCmd())
	disable.AddCommand(disableAzure)
	disableGCP := newDisableGCPProviderCmd()
	disableGCP.AddCommand(newDisableGCPMonitoringCmd())
	disable.AddCommand(disableGCP)

	// --- find ---
	find := newFindCmd()
	find.AddCommand(newFindIntentsCmd())

	// --- verify ---
	verify := newVerifyCmd()
	verify.AddCommand(
		newVerifyOpenPipelineMatcherCmd(),
		newVerifyOpenPipelineDQLProcessorCmd(),
		newVerifyAnalyzerCmd(),
		newVerifyQueryCmd(),
	)

	// --- translate ---
	translate := newTranslateCmd()
	translate.AddCommand(newTranslateLqlToDqlCmd(), newTranslateClassicPipelinesCmd())

	// --- alias ---
	alias := newAliasCmd()
	alias.AddCommand(
		newAliasSetCmd(),
		newAliasListCmd(),
		newAliasDeleteCmd(),
		newAliasExportCmd(),
		newAliasImportCmd(),
	)

	// --- auth ---
	auth := newAuthCmd()
	auth.AddCommand(
		newAuthWhoamiCmd(),
		newAuthStatusCmd(),
		newAuthLoginCmd(),
		newAuthLogoutCmd(),
		newAuthRefreshCmd(),
	)

	// --- config ---
	cfg := newConfigCmd()
	cfg.AddCommand(
		newConfigViewCmd(),
		newConfigInitCmd(),
		newConfigGetContextsCmd(),
		newConfigCurrentContextCmd(),
		newConfigUseContextCmd(),
		newConfigSetCmd(),
		newConfigSetContextCmd(),
		newConfigSetCredentialsCmd(),
		newConfigDeleteCredentialsCmd(),
		newConfigMigrateTokensCmd(),
		newConfigDescribeContextCmd(),
		newConfigDeleteContextCmd(),
		newConfigListDevelopmentCmd(),
	)

	// --- ctx ---
	ctx := newCtxCmd()
	ctx.AddCommand(
		newCtxTokenCmd(),
		newCtxCurrentCmd(),
		newCtxDescribeCmd(),
		newCtxSetCmd(),
		newCtxDeleteCmd(),
	)

	// --- commands (with howto child) ---
	commands := newCommandsCmd()
	commands.AddCommand(newHowtoCmd())

	// --- apply ---
	apply := newApplyCmd()
	apply.AddCommand(newApplyExtensionConfigCmd())

	// --- download ---
	download := newDownloadCmd()
	download.AddCommand(newDownloadExtensionCmd())

	// --- history ---
	history := newHistoryCmd()
	history.AddCommand(
		newHistoryWorkflowCmd(),
		newHistoryDashboardCmd(),
		newHistoryNotebookCmd(),
		newHistoryDocumentCmd(),
	)

	// --- inventory ---
	inventory := newInventoryCmd()
	inventory.AddCommand(newInventoryArrivalsCmd())

	// --- logs ---
	logs := newLogsCmd()
	logs.AddCommand(newLogsWorkflowExecutionCmd())

	// --- open ---
	open := newOpenCmd()
	open.AddCommand(newOpenIntentCmd())

	// --- plugin ---
	plugin := newPluginCmd()
	plugin.AddCommand(newPluginListCmd())

	// --- repo-scope ---
	repoScope := newRepoScopeCmd()
	repoScope.AddCommand(
		newRepoScopeDiscoverCmd(),
		newRepoScopeSetCmd(),
		newRepoScopeCurrentCmd(),
		newRepoScopeDescribeCmd(),
		newRepoScopeDeleteCmd(),
		newRepoScopeListCmd(),
	)

	// --- restore ---
	restore := newRestoreCmd()
	restore.AddCommand(
		newRestoreWorkflowCmd(),
		newRestoreDashboardCmd(),
		newRestoreNotebookCmd(),
		newRestoreDocumentCmd(),
		newRestoreTrashCmd(),
	)

	// --- claim ---
	claim := newClaimCmd()
	claim.AddCommand(newClaimEnvironmentShareCmd())

	// --- share / unshare ---
	share := newShareCmd()
	share.AddCommand(newShareDocumentCmd(), newShareNotebookCmd(), newShareDashboardCmd())
	unshare := newUnshareCmd()
	unshare.AddCommand(newUnshareDocumentCmd(), newUnshareNotebookCmd(), newUnshareDashboardCmd())

	// --- skills ---
	skills := newSkillsCmd()
	skills.AddCommand(newSkillsInstallCmd(), newSkillsUninstallCmd(), newSkillsStatusCmd())

	// --- wait ---
	wait := newWaitCmd()
	wait.AddCommand(newWaitQueryCmd())

	// Wire all verbs onto root
	root.AddCommand(
		alias, apply, auth, claim, commands, newCompletionCmd(), cfg, create, ctx,
		del, describe, newDiffCmd(), disable, newDoctorCmd(), download, edit,
		enable, exec, find, get, history, newInspectCmd(), inventory, logs,
		open, plugin, newQueryCmd(), repoScope, restore, share, unshare, skills,
		newTokenScopesHelpTopicCmd(), translate, update, verify,
		newVersionCmd(), wait,
	)
	bindDryRunFlags(root, &inv.flags)

	// cobra hands a subcommand its parent's context only once it executes, and
	// the code that prepares a tree before running it (profile, stability
	// floor, blocked commands) asks each command for its context. Give every
	// command the invocation's now, so none answers with an empty one.
	walkCommands(root, func(c *cobra.Command) { c.SetContext(ictx) })

	return &commandTree{root: root, get: get}
}

// concurrentInvocation returns the invocation ctx carries when it is a
// concurrent one, else nil. Code that must choose between
// per-invocation storage and the process singleton's asks this.
func concurrentInvocation(ctx context.Context) *invocation {
	if inv := current(ctx); inv != nil && inv.concurrent {
		return inv
	}
	return nil
}

// registerFlagCompletion registers a shell-completion function for a flag,
// on the singleton tree only. Cobra keeps these in a package-level map keyed
// by *pflag.Flag that nothing ever prunes, so registering one on a
// per-invocation tree would keep that whole tree reachable for the life of the
// process. Completion is an interactive-CLI feature; no request asks for it.
//
// The singleton's constructors all run during package variable
// initialization, which Go completes before any init() function runs, so
// "after init" means "building some other tree". A flag answers that without
// a context, which a command being built does not have yet.
func registerFlagCompletion(c *cobra.Command, flag string, fn cobra.CompletionFunc) {
	if singletonBuilt.Load() {
		return
	}
	_ = c.RegisterFlagCompletionFunc(flag, fn)
}

// singletonBuilt is set once the package-level command values exist. See
// registerFlagCompletion.
var singletonBuilt atomic.Bool

func init() { singletonBuilt.Store(true) }

// registerRootPersistentFlags adds the root persistent flags to c, binding
// each value to the corresponding field in flags. Pass &gFlags for the
// singleton tree; pass &inv.flags for a per-invocation tree so concurrent
// parse phases never race on the same memory.
func registerRootPersistentFlags(c *cobra.Command, flags *rootFlags) {
	singleton := flags == &gFlags

	c.PersistentFlags().StringVar(&flags.cfgFile, "config", "", "config file (searches .dtctl.yaml upward, then $XDG_CONFIG_HOME/dtctl/config)")

	// contextName is a package-level var; binding it on every fresh tree would
	// race across concurrent newCommandTree calls. On fresh trees we bind a
	// discard var instead: service invocations use a Session that replaces
	// context resolution wholesale, so --context is parsed but never consulted.
	if singleton {
		c.PersistentFlags().StringVar(&contextName, "context", "", "use a specific context for this invocation (env: DTCTL_CONTEXT; never persisted)")
	} else {
		var discardContext string
		c.PersistentFlags().StringVar(&discardContext, "context", "", "use a specific context for this invocation (env: DTCTL_CONTEXT; never persisted)")
	}

	c.PersistentFlags().StringVarP(&flags.outputFormat, "output", "o", "table", "output format: json|yaml|csv|toon|table|wide|auto")
	c.PersistentFlags().StringVar(&flags.jqFilter, "jq", "", "jq filter expression for structured output (json|yaml|toon); applied to the result payload, not the --agent envelope (on query: '.records', not '.result.records'); non-structured formats are auto-promoted to json")
	c.PersistentFlags().CountVarP(&flags.verbosity, "verbose", "v", "verbose output (-v for details, -vv for full debug including auth headers)")
	c.PersistentFlags().BoolVar(&flags.debugMode, "debug", false, "enable debug mode (full HTTP request/response logging, equivalent to -vv)")
	c.PersistentFlags().BoolVar(&flags.plainMode, "plain", false, "plain output for machine processing (no colors, no interactive prompts)")
	c.PersistentFlags().BoolVarP(&flags.agentMode, "agent", "A", false, "agent output mode: wrap output in a structured JSON envelope with metadata")
	c.PersistentFlags().BoolVar(&flags.noAgent, "no-agent", false, "disable auto-detected agent mode")
	c.PersistentFlags().BoolVar(&flags.checkScopes, "check-scopes", false, "check the active token has the scopes this command requires, then exit without running it")
	c.PersistentFlags().Int64Var(&flags.chunkSize, "chunk-size", 500, "Paginate through all results in chunks of this size. 0 returns only the first page.")
	rejectEmptyFlag(c, "context")
	rejectEmptyFlag(c, "config")

	// Viper bindings are only needed for the singleton (CLI) path; fresh trees
	// read all values through curFlags() on the invocation, not through viper.
	// Registering them concurrently would race on the global viper instance.
	if singleton {
		_ = viper.BindPFlag("context", c.PersistentFlags().Lookup("context"))
		_ = viper.BindPFlag("output", c.PersistentFlags().Lookup("output"))
		_ = viper.BindPFlag("verbose", c.PersistentFlags().Lookup("verbose"))
	}

	// Cobra's default usage template with bold headers; re-diff it on a Cobra upgrade.
	c.SetUsageTemplate(`{{bold "Usage:"}}{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

{{bold "Aliases:"}}
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

{{bold "Examples:"}}
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

{{bold "Available Commands:"}}{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{bold .Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

{{bold "Additional Commands:"}}{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

{{bold "Flags:"}}
{{flagUsages .LocalFlags | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

{{bold "Global Flags:"}}
{{flagUsages .InheritedFlags | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

{{bold "Additional help topics:"}}{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`)
}
