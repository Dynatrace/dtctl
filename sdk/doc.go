// Package sdk is the dt-cli-sdk — a shared library of Dynatrace CLI primitives.
//
// It provides platform-level building blocks that multiple Dynatrace CLIs
// (dtctl, dtmgd, dtwiz, and others) can share instead of re-implementing:
//
//   - urls: Dynatrace environment URL validation and correction
//   - auth: Token type detection and classification
//   - httpclient: HTTP client with retry, pagination, and typed errors
//   - agentmode: AI agent detection and structured JSON envelope
//   - inventory: environment data-inventory discovery (capability evidence,
//     entity census, buckets) driven through a caller-supplied DQL runner
//
// The SDK is designed to be leaf-shaped: it never imports from the CLI
// module, has minimal dependencies, and avoids global state.
//
// See https://github.com/Dynatrace/dtctl/tree/main/sdk for details.
package sdk
