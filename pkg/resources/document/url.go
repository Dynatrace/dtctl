package document

import (
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// viewerPaths maps a document type to the app path that renders it, relative
// to "<base>/ui/apps/". The app ID cannot be derived from the type: launchpads
// are served by the "dynatrace.launcher" app, and "dynatrace.launchpads"
// returns 404.
var viewerPaths = map[string]string{
	"dashboard": "dynatrace.dashboards/dashboard",
	"notebook":  "dynatrace.notebooks/notebook",
	"launchpad": "dynatrace.launcher/launchpad",
}

// UIURL returns the URL at which the environment's UI shows the document, or ""
// when the document type has no known viewer app. Custom document types (e.g.
// "acme:config") are not served by an app whose ID follows from the type, so
// no URL is better than a broken guess. The ID is escaped as one path segment,
// so a caller-chosen ID containing "/", "?", "#" or ":" cannot address a
// different route or lose its tail. Trailing slashes on baseURL are dropped, so
// an environment configured as "https://host/" does not yield "//ui/".
func UIURL(baseURL, docType, id string) string {
	path, ok := viewerPaths[docType]
	if !ok || id == "" {
		return ""
	}
	return fmt.Sprintf("%s/ui/apps/%s/%s", strings.TrimRight(baseURL, "/"), path, httpclient.PathSegment(id))
}
