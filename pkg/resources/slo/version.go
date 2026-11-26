package slo

import (
	"time"

	sdkslo "github.com/Dynatrace/dtctl/sdk/api/slo"
)

// DecodeVersionTimestamp extracts the last-modified timestamp from a Dynatrace SLO version string.
// The version is a RawURLEncoding base64-encoded binary structure containing a v1 UUID revision.
// Returns nil if the version cannot be decoded or does not contain a v1 UUID.
func DecodeVersionTimestamp(version string) *time.Time {
	return sdkslo.DecodeVersionTimestamp(version)
}
