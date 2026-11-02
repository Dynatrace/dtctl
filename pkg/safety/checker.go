// Package safety re-exports the safety-level semantics from
// github.com/Dynatrace/dtctl/sdk/session, where the implementation moved:
// a readonly context must mean the same thing in dtctl and every dtctl-*
// plugin, so the checker is part of
// the shared session layer. This package remains so the root module's import
// surface is stable; new code should import sdk/session directly.
package safety

import (
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/sdk/session"
)

type (
	Operation         = session.Operation
	ResourceOwnership = session.ResourceOwnership
	CheckResult       = session.CheckResult
	Checker           = session.Checker
	SafetyError       = session.SafetyError
)

const (
	OperationRead         = session.OperationRead
	OperationCreate       = session.OperationCreate
	OperationUpdate       = session.OperationUpdate
	OperationDelete       = session.OperationDelete
	OperationDeleteBucket = session.OperationDeleteBucket

	OwnershipUnknown = session.OwnershipUnknown
	OwnershipOwn     = session.OwnershipOwn
	OwnershipShared  = session.OwnershipShared
)

func NewChecker(contextName string, ctx *config.Context) *Checker {
	return session.NewChecker(contextName, ctx)
}

func NewCheckerWithLevel(contextName string, level config.SafetyLevel) *Checker {
	return session.NewCheckerWithLevel(contextName, level)
}

func DetermineOwnership(resourceOwnerID, currentUserID string) ResourceOwnership {
	return session.DetermineOwnership(resourceOwnerID, currentUserID)
}
