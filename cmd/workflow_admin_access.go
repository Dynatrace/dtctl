package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// workflowAdminAccessSince is the release that introduced the workflow
// commands' --admin-access (#700).
const workflowAdminAccessSince = "0.44.0"

// adminScopeAnnotation, on an --admin-access flag, names the scope the flag
// needs. A 403 on a run with the flag set is then rewritten by
// adminAccessError into one that names both gates.
const adminScopeAnnotation = "dtctl/admin-scope"

// addWorkflowAdminAccessFlag declares --admin-access on a workflow or
// workflow-execution command: every Automation API call the command makes then
// sends adminAccess=true, the API side of the Workflows app's "Workflow admin"
// mode. Without it the API answers a workflow the caller neither owns nor was
// shared with a 403 "Only workflow owner can perform this action" — whatever
// the safety level, which only gates what dtctl itself will attempt.
func addWorkflowAdminAccessFlag(c *cobra.Command) {
	c.Flags().Bool("admin-access", false,
		"act on any workflow in the environment, not only your own (Workflow admin mode); requires the "+auth.WorkflowAdminScope+" scope and IAM permission")
	_ = c.Flags().SetAnnotation("admin-access", adminScopeAnnotation, []string{auth.WorkflowAdminScope})
	stability.MarkFlag(c, "admin-access", stability.Experimental, workflowAdminAccessSince)
}

// workflowAdminAccess reports whether the command was run with --admin-access.
// It is false on a command that does not declare the flag.
func workflowAdminAccess(cmd *cobra.Command) bool {
	on, err := cmd.Flags().GetBool("admin-access")
	return err == nil && on
}

// adminAccessError rewrites a 403 from a run with an annotated --admin-access
// set into the error adminAccessDenied builds. Any other error, and any 403 on
// a run without the flag, passes through untouched: that 403 is some other
// denial, and admin-access advice would be wrong for it.
//
// It is applied once, around every RunE (installScopePreflight), rather than by
// each command, so that a command's RunE stays its own function.
func adminAccessError(c *cobra.Command, err error) error {
	if err == nil || !isPermissionDenied(err) {
		return err
	}
	f := c.Flags().Lookup("admin-access")
	if f == nil || len(f.Annotations[adminScopeAnnotation]) == 0 {
		return err
	}
	if on, getErr := c.Flags().GetBool("admin-access"); getErr != nil || !on {
		return err
	}
	var cfg *config.Config
	if loaded, loadErr := loadConfig(cmdContext(c)); loadErr == nil {
		cfg = loaded
	}
	return adminAccessDenied(err, cfg, f.Annotations[adminScopeAnnotation][0], c.CommandPath()+" --admin-access")
}
