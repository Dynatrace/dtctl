package query

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// recordSink dispatches a decoded row to the top-level or result-nested
// callback, with the response state decoded so far ("" until "state" is seen).
type recordSink struct {
	onTop    func(state string, row map[string]interface{}) error
	onResult func(state string, row map[string]interface{}) error
}

// ignoreState adapts a state-less row callback to a recordSink callback.
func ignoreState(onRecord func(map[string]interface{}) error) func(string, map[string]interface{}) error {
	if onRecord == nil {
		return nil
	}
	return func(_ string, row map[string]interface{}) error { return onRecord(row) }
}

// ExecuteStream is Execute but decodes incrementally, calling onRecord per row instead of accumulating.
// Response.Records / Result.Records are always nil; onRecord may be nil to just discard rows.
func (h *Handler) ExecuteStream(ctx context.Context, req ExecuteRequest, onRecord func(map[string]interface{}) error) (*Response, error) {
	cb := ignoreState(onRecord)
	resp, err := h.executeRaw(ctx, req, recordSink{onTop: cb, onResult: cb})
	if err != nil {
		return nil, err
	}
	resp.Records = nil
	if resp.Result != nil {
		resp.Result.Records = nil
	}
	return resp, nil
}

// PollStream is Poll but streams like ExecuteStream — same onRecord/nil-Records contract.
func (h *Handler) PollStream(ctx context.Context, requestToken string, timeoutMs int64, enrich bool, onRecord func(map[string]interface{}) error) (*Response, error) {
	cb := ignoreState(onRecord)
	resp, err := h.pollRaw(ctx, requestToken, timeoutMs, enrich, recordSink{onTop: cb, onResult: cb})
	if err != nil {
		return nil, err
	}
	resp.Records = nil
	if resp.Result != nil {
		resp.Result.Records = nil
	}
	return resp, nil
}

// executeRaw performs query:execute and streams the response through sink.
// Response.Records / Result.Records come back as a non-nil presence marker only, never populated — see Execute/ExecuteStream.
func (h *Handler) executeRaw(ctx context.Context, req ExecuteRequest, sink recordSink) (*Response, error) {
	httpReq := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept-Encoding", "identity"). // RawBody() below must see the plain body, not gzip.
		SetBody(req).
		SetDoNotParseResponse(true)
	if req.EnrichMetricMetadata {
		httpReq.SetQueryParam("enrich", enrichMetricMetadataParam)
	}
	h.applyHeaders(httpReq)

	resp, err := httpReq.Post(basePath + ":execute")
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer func() { _ = resp.RawBody().Close() }()

	// 200 with completed state or 202 with RUNNING are both valid.
	if resp.StatusCode() == 200 || resp.StatusCode() == 202 {
		result, err := decodeResponseStream(resp.RawBody(), sink)
		if err != nil {
			return nil, fmt.Errorf("failed to execute query: %w", err)
		}
		return result, nil
	}

	if resp.IsError() {
		body, _ := io.ReadAll(resp.RawBody())
		return nil, parseError(resp.StatusCode(), body)
	}
	return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode())
}

// pollRaw performs query:poll and streams the response — see executeRaw for the accumulation contract.
func (h *Handler) pollRaw(ctx context.Context, requestToken string, timeoutMs int64, enrich bool, sink recordSink) (*Response, error) {
	httpReq := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("request-token", requestToken).
		SetQueryParam("request-timeout-milliseconds", fmt.Sprintf("%d", timeoutMs)).
		SetHeader("Accept-Encoding", "identity"). // RawBody() below must see the plain body, not gzip.
		SetDoNotParseResponse(true)
	if enrich {
		httpReq.SetQueryParam("enrich", enrichMetricMetadataParam)
	}
	h.applyHeaders(httpReq)

	resp, err := httpReq.Get(basePath + ":poll")
	if err != nil {
		return nil, fmt.Errorf("failed to poll query: %w", err)
	}
	defer func() { _ = resp.RawBody().Close() }()

	if resp.IsError() {
		body, _ := io.ReadAll(resp.RawBody())
		return nil, httpclient.NewAPIError(resp.StatusCode(), resp.Status(), string(body))
	}

	result, err := decodeResponseStream(resp.RawBody(), sink)
	if err != nil {
		return nil, fmt.Errorf("failed to poll query: %w", err)
	}
	return result, nil
}

// decodeResponseStream decodes a query Execute/Poll response from r, streaming records
// through sink instead of buffering them. Keys not listed here are decoded and discarded.
// Keep in sync with Response/Result in query.go — see TestDecodeResponseStream_AllFields.
func decodeResponseStream(r io.Reader, sink recordSink) (*Response, error) {
	dec := json.NewDecoder(r)

	if err := expectDelim(dec, '{'); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var resp Response
	for dec.More() {
		key, err := decodeObjectKey(dec)
		if err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		switch key {
		case "state":
			if err := dec.Decode(&resp.State); err != nil {
				return nil, fmt.Errorf("decode response.state: %w", err)
			}
		case "requestToken":
			if err := dec.Decode(&resp.RequestToken); err != nil {
				return nil, fmt.Errorf("decode response.requestToken: %w", err)
			}
		case "progress":
			if err := dec.Decode(&resp.Progress); err != nil {
				return nil, fmt.Errorf("decode response.progress: %w", err)
			}
		case "metadata":
			if err := dec.Decode(&resp.Metadata); err != nil {
				return nil, fmt.Errorf("decode response.metadata: %w", err)
			}
		case "records":
			present, err := streamRecordsArray(dec, resp.State, sink.onTop)
			if err != nil {
				return nil, fmt.Errorf("decode response.records: %w", err)
			}
			if present {
				resp.Records = []map[string]interface{}{}
			}
		case "result":
			result, err := decodeResultStream(dec, resp.State, sink.onResult)
			if err != nil {
				return nil, fmt.Errorf("decode response.result: %w", err)
			}
			resp.Result = result
		default:
			if err := discardValue(dec); err != nil {
				return nil, fmt.Errorf("decode response.%s: %w", key, err)
			}
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if err := expectEOF(dec); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &resp, nil
}

// decodeResultStream decodes a "result" object, streaming its nested "records" through onRecord.
// dec must be positioned right after the "result" key token. Returns (nil, nil) for a JSON null.
func decodeResultStream(dec *json.Decoder, state string, onRecord func(string, map[string]interface{}) error) (*Result, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return nil, fmt.Errorf("expected object, got %v", tok)
	}

	var result Result
	for dec.More() {
		key, err := decodeObjectKey(dec)
		if err != nil {
			return nil, err
		}

		switch key {
		case "records":
			present, err := streamRecordsArray(dec, state, onRecord)
			if err != nil {
				return nil, err
			}
			if present {
				result.Records = []map[string]interface{}{}
			}
		case "types":
			if err := dec.Decode(&result.Types); err != nil {
				return nil, err
			}
		case "metadata":
			if err := dec.Decode(&result.Metadata); err != nil {
				return nil, err
			}
		default:
			if err := discardValue(dec); err != nil {
				return nil, err
			}
		}
	}

	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	return &result, nil
}

// streamRecordsArray decodes a JSON array of rows, invoking onRecord per row instead of accumulating.
// present=false means the array was JSON null; present=true with zero calls means "[]".
func streamRecordsArray(dec *json.Decoder, state string, onRecord func(string, map[string]interface{}) error) (present bool, err error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if tok == nil {
		return false, nil
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '[' {
		return false, fmt.Errorf("expected array, got %v", tok)
	}

	for dec.More() {
		var row map[string]interface{}
		if err := dec.Decode(&row); err != nil {
			return false, err
		}
		if onRecord != nil {
			if err := onRecord(state, row); err != nil {
				return false, err
			}
		}
	}

	if err := expectDelim(dec, ']'); err != nil {
		return false, err
	}
	return true, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// expectEOF requires no further tokens remain in dec, rejecting trailing data after a decoded value.
func expectEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing data after response")
		}
		return err
	}
	return nil
}

func decodeObjectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected object key, got %v", tok)
	}
	return key, nil
}

func discardValue(dec *json.Decoder) error {
	var discard json.RawMessage
	return dec.Decode(&discard)
}
