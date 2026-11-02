package apply

import (
	"errors"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// lookupError turns an existence-lookup error into the error that must stop the
// apply, or nil when the object genuinely does not exist.
//
// Only a 404 means "create". A 403, a 5xx or a network error says nothing about
// whether the object exists, and treating it as absent creates a duplicate for
// every resource whose create request does not carry the id.
func lookupError(resourceType, id string, err error) error {
	if err == nil || errors.Is(err, httpclient.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("failed to check %s %q existence: %w", resourceType, id, err)
}

// nameLookupError is lookupError for the resources that resolve create vs
// update by searching a list for a name or title. A list call has no "absent"
// status code: an empty result means absent, and any error means unknown.
func nameLookupError(resourceType, name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("failed to check %s %q existence: %w", resourceType, name, err)
}
