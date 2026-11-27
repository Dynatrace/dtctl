package cmd

import (
	"errors"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// errorText renders err for a user or an agent. httpclient.APIError keeps the
// upstream response body out of Error() (it sits in Details), so the CLI reads
// it explicitly: the body is where the server names the failing field, the
// missing scope or the errorRef support asks for.
//
// A diagnostic.Error owns its own rendering (operation, advice) and is left
// alone; the handlers that build one from an APIError add the details
// themselves.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	var diagErr *diagnostic.Error
	if errors.As(err, &diagErr) {
		return err.Error()
	}
	return httpclient.ErrorText(err)
}
