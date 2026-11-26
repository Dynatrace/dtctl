package cmd

import (
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/resources/document"
)

// requireDocumentType refuses a document whose type is not the one the command
// names. A UUID-shaped argument is taken as an ID without a lookup, so
// `delete dashboard <notebook-id>` would otherwise act on the notebook. Every
// typed dashboard/notebook command already fetches the metadata after resolving,
// so checking it here costs no extra request and runs before any write.
func requireDocumentType(metadata *document.DocumentMetadata, want, id string) error {
	if metadata.Type == want {
		return nil
	}
	return fmt.Errorf("document %q is a %s, not a %s; use 'document' instead of '%s' to address it", id, metadata.Type, want, want)
}
