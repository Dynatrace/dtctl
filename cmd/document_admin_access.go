package cmd

import (
	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
)

// listDocuments lists documents and, when --admin-access is what the API
// refused, replaces the bare 403 with one that says which gate to check.
func listDocuments(h *document.Handler, filters document.DocumentFilters, cfg *config.Config) (*document.DocumentList, error) {
	list, err := h.List(filters)
	if err != nil && filters.AdminAccess && isPermissionDenied(err) {
		return nil, adminAccessDenied(err, cfg, auth.DocumentAdminScope, "list documents with --admin-access")
	}
	return list, err
}
