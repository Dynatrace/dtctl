// Package previewprocessor is the CLI resource layer for the OpenPipeline
// preview processor endpoint. It delegates HTTP calls to the SDK and shapes
// the response into display-ready PreviewResult entries with table tags.
//
// File reading (the --file flag) is handled here rather than in the SDK, in
// line with the dtctl delegation contract.
package previewprocessor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
	sdkpreview "github.com/dynatrace-oss/dtctl/sdk/api/previewprocessor"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// PreviewResult is the CLI read-model for a single preview result entry.
type PreviewResult struct {
	Matched bool           `json:"matched" yaml:"matched" table:"MATCHED"`
	Record  map[string]any `json:"record,omitempty" yaml:"record,omitempty" table:"RECORD"`
}

// Handler previews OpenPipeline processor definitions against sample records.
type Handler struct {
	sdk    *sdkpreview.Handler
	vfsEnv vfs.Env
}

// NewHandler creates a new preview processor handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkpreview.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// WithVFS sets the filesystem and stdin Preview reads the definition from. The
// zero Env keeps the process's.
func (h *Handler) WithVFS(env vfs.Env) *Handler {
	h.vfsEnv = env
	return h
}

// Preview reads a processor definition body from the given file path ("-" for
// stdin), wraps it in the preview request envelope (adding configID when set),
// sends it to the preview endpoint, and returns the display-ready results.
//
// The file is the processor body itself (e.g. {"type":"dql","dqlScript":...,
// "sampleData":...}) — the same shape a processor has inside a pipeline config —
// not the transport envelope. dtctl builds {"processor":<body>,"configId":...}
// around it, so callers never hand-write the wrapper.
func (h *Handler) Preview(filename, configID string) ([]PreviewResult, error) {
	body, err := readFileOrStdin(h.vfsEnv, filename)
	if err != nil {
		return nil, err
	}

	envelope, err := buildEnvelope(body, configID)
	if err != nil {
		return nil, err
	}

	sdkResult, err := h.sdk.Preview(context.Background(), envelope)
	if err != nil {
		return nil, err
	}

	results := make([]PreviewResult, len(sdkResult.Results))
	for i, r := range sdkResult.Results {
		results[i] = PreviewResult{
			Matched: r.Matched,
			Record:  r.Record,
		}
	}
	return results, nil
}

// buildEnvelope wraps a processor body in the preview request envelope. The
// processor bytes are embedded verbatim (the processor is a oneOf union dtctl
// deliberately does not interpret); configID is added as the top-level
// "configId" only when non-empty. It errors if the body is not a JSON object.
func buildEnvelope(processor json.RawMessage, configID string) (json.RawMessage, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(processor, &probe); err != nil {
		return nil, fmt.Errorf("processor definition must be a JSON object: %w", err)
	}

	env := map[string]json.RawMessage{"processor": processor}
	if configID != "" {
		idBytes, err := json.Marshal(configID)
		if err != nil {
			return nil, err
		}
		env["configId"] = idBytes
	}

	out, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("build preview request: %w", err)
	}
	return out, nil
}

// readFileOrStdin reads a file (through the vfs seam, so embedded invocations
// resolve virtual request files; "-" means stdin) and returns the content as
// json.RawMessage after a basic JSON validity check.
func readFileOrStdin(env vfs.Env, filename string) (json.RawMessage, error) {
	data, err := env.ReadFileOrStdin(filename)
	if err != nil {
		return nil, fmt.Errorf("read processor definition: %w", err)
	}

	if !json.Valid(data) {
		return nil, fmt.Errorf("processor definition is not valid JSON")
	}

	return json.RawMessage(data), nil
}
