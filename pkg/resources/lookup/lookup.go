package lookup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/exec"
)

// Handler handles lookup table resources
type Handler struct {
	client *client.Client
}

// NewHandler creates a new lookup handler
func NewHandler(c *client.Client) *Handler {
	return &Handler{client: c}
}

// Lookup represents a lookup table file
type Lookup struct {
	Path        string   `json:"path" table:"PATH"`
	DisplayName string   `json:"displayName,omitempty" table:"DISPLAY_NAME"`
	Description string   `json:"description,omitempty" table:"DESCRIPTION,wide"`
	FileSize    int64    `json:"fileSize,omitempty" table:"SIZE"`
	Records     int      `json:"records,omitempty" table:"RECORDS"`
	LookupField string   `json:"lookupField,omitempty" table:"LOOKUP_FIELD,wide"`
	Columns     []string `json:"columns,omitempty" table:"-"`
	Modified    string   `json:"modified,omitempty" table:"MODIFIED"`
}

// LookupData represents a lookup table with its data
type LookupData struct {
	Lookup
	Data []map[string]interface{} `json:"data"`
}

// CreateRequest represents a request to create a lookup table
type CreateRequest struct {
	FilePath       string
	DisplayName    string
	Description    string
	LookupField    string
	ParsePattern   string
	SkippedRecords int
	AutoFlatten    bool
	Timezone       string
	Locale         string
	Overwrite      bool
	// DataContent is the payload to upload, and is required. Reading it from
	// a file or from stdin is the caller's job: this package does no file I/O.
	DataContent []byte
}

// UploadRequest represents the JSON request body for upload
type UploadRequest struct {
	FilePath       string `json:"filePath"`
	DisplayName    string `json:"displayName,omitempty"`
	Description    string `json:"description,omitempty"`
	LookupField    string `json:"lookupField"`
	ParsePattern   string `json:"parsePattern"`
	SkippedRecords int    `json:"skippedRecords"`
	AutoFlatten    bool   `json:"autoFlatten"`
	Timezone       string `json:"timezone,omitempty"`
	Locale         string `json:"locale,omitempty"`
	Overwrite      bool   `json:"overwrite"`
}

// UploadResponse represents the response from upload
type UploadResponse struct {
	FileSize            int64 `json:"fileSize"`
	UploadedBytes       int64 `json:"uploadedBytes"`
	PatternMatches      int   `json:"patternMatches"`
	SkippedRecords      int   `json:"skippedRecords"`
	DiscardedDuplicates int   `json:"discardedDuplicates"`
	Records             int   `json:"records"`

	// InputRecords is the number of data records dtctl sent, filled in
	// client-side (the API does not return it). It is the baseline the
	// server-side counts above are checked against.
	InputRecords int `json:"inputRecords,omitempty"`
	// ParsePattern is the DPL pattern the content was uploaded with, filled
	// in client-side so callers can report the auto-detected one.
	ParsePattern string `json:"parsePattern,omitempty"`
}

// CheckRecordCount compares the server-side record counts against the number
// of records dtctl uploaded. The upload API answers 2xx even when the parse
// pattern matched nothing, so a near-total parse failure otherwise looks like
// a successful create (issue #471).
//
// It returns a warning for a partial loss and an error when nothing at all was
// stored. Both are empty when InputRecords is unknown or everything is
// accounted for.
func (r *UploadResponse) CheckRecordCount() (warning string, err error) {
	if r.InputRecords <= 0 {
		return "", nil
	}

	accounted := r.Records + r.DiscardedDuplicates
	if accounted >= r.InputRecords {
		return "", nil
	}

	if r.Records == 0 {
		return "", fmt.Errorf("none of the %d uploaded records were stored: the parse pattern matched no line; check the input format or adjust --parse-pattern", r.InputRecords)
	}

	return fmt.Sprintf("only %d of %d records were stored; %d line(s) did not match the parse pattern", r.Records, r.InputRecords, r.InputRecords-accounted), nil
}

// DeleteRequest represents a request to delete a lookup table
type DeleteRequest struct {
	FilePath string `json:"filePath"`
}

// List lists all lookup tables
func (h *Handler) List() ([]Lookup, error) {
	// Query all files in the system (note: the path field is called 'name' in dt.system.files)
	query := `fetch dt.system.files | filter startsWith(name, "/lookups/")`

	executor := exec.NewDQLExecutor(h.client)
	result, err := executor.ExecuteQuery(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list lookup tables: %w", err)
	}

	// Parse records into Lookup structs
	var lookups []Lookup
	records := result.Records
	if result.Result != nil {
		records = result.Result.Records
	}

	for _, record := range records {
		lookup := Lookup{}

		// Extract path (field is called 'name' in dt.system.files)
		if name, ok := record["name"].(string); ok {
			lookup.Path = name
		}

		// Extract other metadata fields if available
		if displayName, ok := record["display_name"].(string); ok {
			lookup.DisplayName = displayName
		}
		if description, ok := record["description"].(string); ok {
			lookup.Description = description
		}
		// Handle size field (can be float64 or string)
		if size, ok := record["size"].(float64); ok {
			lookup.FileSize = int64(size)
		} else if sizeStr, ok := record["size"].(string); ok {
			if size, err := parseIntFromString(sizeStr); err == nil {
				lookup.FileSize = int64(size)
			}
		}
		// Handle records field (can be float64 or string)
		if records, ok := record["records"].(float64); ok {
			lookup.Records = int(records)
		} else if recordsStr, ok := record["records"].(string); ok {
			if records, err := parseIntFromString(recordsStr); err == nil {
				lookup.Records = records
			}
		}
		if modified, ok := record["modified.timestamp"].(string); ok {
			lookup.Modified = formatTimestamp(modified)
		}

		lookups = append(lookups, lookup)
	}

	return lookups, nil
}

// Get retrieves a specific lookup table metadata and preview data
func (h *Handler) Get(path string) (*Lookup, error) {
	// Validate path
	if err := ValidatePath(path); err != nil {
		return nil, err
	}

	executor := exec.NewDQLExecutor(h.client)

	// First, get metadata from dt.system.files
	metadataQuery := fmt.Sprintf(`fetch dt.system.files | filter name == "%s"`, path)
	metadataResult, err := executor.ExecuteQuery(metadataQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to get lookup table metadata %q: %w", path, err)
	}

	metadataRecords := metadataResult.Records
	if metadataResult.Result != nil {
		metadataRecords = metadataResult.Result.Records
	}

	if len(metadataRecords) == 0 {
		return nil, fmt.Errorf("lookup table %q not found", path)
	}

	lookup := &Lookup{
		Path: path,
	}

	// Extract metadata if available
	if len(metadataRecords) > 0 {
		record := metadataRecords[0]

		if displayName, ok := record["display_name"].(string); ok {
			lookup.DisplayName = displayName
		}
		if description, ok := record["description"].(string); ok {
			lookup.Description = description
		}
		// Handle size field (can be float64 or string)
		if size, ok := record["size"].(float64); ok {
			lookup.FileSize = int64(size)
		} else if sizeStr, ok := record["size"].(string); ok {
			if size, err := parseIntFromString(sizeStr); err == nil {
				lookup.FileSize = int64(size)
			}
		}
		// Handle records field (can be float64 or string)
		if records, ok := record["records"].(float64); ok {
			lookup.Records = int(records)
		} else if recordsStr, ok := record["records"].(string); ok {
			if records, err := parseIntFromString(recordsStr); err == nil {
				lookup.Records = records
			}
		}
		if modified, ok := record["modified.timestamp"].(string); ok {
			lookup.Modified = formatTimestamp(modified)
		}
	}

	// Use DQL to load the lookup and get schema
	query := fmt.Sprintf("load \"%s\" | limit 1", path)
	result, err := executor.ExecuteQuery(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get lookup table %q: %w", path, err)
	}

	// Extract column names from first record
	records := result.Records
	if result.Result != nil {
		records = result.Result.Records
	}

	if len(records) > 0 {
		// Extract column names
		for col := range records[0] {
			lookup.Columns = append(lookup.Columns, col)
		}
	}

	return lookup, nil
}

// maxLookupRecords is the maximum number of records to retrieve from a lookup table.
// The DQL API defaults to 1000 records if not specified, which silently truncates
// large lookup tables. We set this to 1,000,000 to effectively retrieve all records.
const maxLookupRecords = 1_000_000

// GetDataResult contains the data records and any DQL notifications
type GetDataResult struct {
	Records       []map[string]interface{}
	Notifications []exec.QueryNotification
}

// GetData retrieves the full data of a lookup table
func (h *Handler) GetData(path string, limit int) (*GetDataResult, error) {
	// Validate path
	if err := ValidatePath(path); err != nil {
		return nil, err
	}

	// Use DQL to load the lookup
	query := fmt.Sprintf("load \"%s\"", path)
	if limit > 0 {
		query += fmt.Sprintf(" | limit %d", limit)
	}

	// Set a high MaxResultRecords to avoid silent truncation at the DQL default of 1000.
	opts := exec.DQLExecuteOptions{
		MaxResultRecords: maxLookupRecords,
	}

	executor := exec.NewDQLExecutor(h.client)
	result, err := executor.ExecuteQueryWithOptions(query, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get lookup data %q: %w", path, err)
	}

	records := result.Records
	if result.Result != nil {
		records = result.Result.Records
	}

	return &GetDataResult{
		Records:       records,
		Notifications: result.GetNotifications(),
	}, nil
}

// GetWithData retrieves a lookup table with its data
func (h *Handler) GetWithData(path string, limit int) (*LookupData, []exec.QueryNotification, error) {
	lookup, err := h.Get(path)
	if err != nil {
		return nil, nil, err
	}

	dataResult, err := h.GetData(path, limit)
	if err != nil {
		return nil, nil, err
	}

	return &LookupData{
		Lookup: *lookup,
		Data:   dataResult.Records,
	}, dataResult.Notifications, nil
}

// Create creates a new lookup table
func (h *Handler) Create(req CreateRequest) (*UploadResponse, error) {
	// Validate path
	if err := ValidatePath(req.FilePath); err != nil {
		return nil, err
	}

	dataContent := req.DataContent
	if len(dataContent) == 0 {
		return nil, fmt.Errorf("no data content specified")
	}

	// Auto-detect parse pattern for CSV if not specified
	var inputRecords int
	if req.ParsePattern == "" {
		prepared, err := PrepareCSV(dataContent)
		if err != nil {
			return nil, fmt.Errorf("failed to detect CSV pattern: %w", err)
		}
		req.ParsePattern = prepared.Pattern
		req.SkippedRecords = prepared.SkippedRecords
		// PrepareCSV rewrites quoted or ragged CSV so the pattern above can
		// actually match it; for well-formed input Content is the original.
		dataContent = prepared.Content
		inputRecords = prepared.DataRecords
	} else {
		// With a caller-supplied pattern dtctl does not know the format, so
		// the line count is the only baseline available.
		inputRecords = max(countDataRecords(dataContent)-req.SkippedRecords, 0)
	}

	// Set defaults
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	if req.Locale == "" {
		req.Locale = "en_US"
	}
	req.AutoFlatten = true // Always true by default

	// Create multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add request JSON part
	requestJSON := UploadRequest{
		FilePath:       req.FilePath,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		LookupField:    req.LookupField,
		ParsePattern:   req.ParsePattern,
		SkippedRecords: req.SkippedRecords,
		AutoFlatten:    req.AutoFlatten,
		Timezone:       req.Timezone,
		Locale:         req.Locale,
		Overwrite:      req.Overwrite,
	}

	requestBytes, err := json.Marshal(requestJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	part, err := writer.CreateFormField("request")
	if err != nil {
		return nil, fmt.Errorf("failed to create form field: %w", err)
	}
	if _, err := part.Write(requestBytes); err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	// Add content part
	fileName := filepath.Base(req.FilePath)
	part, err = writer.CreateFormFile("content", fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := part.Write(dataContent); err != nil {
		return nil, fmt.Errorf("failed to write content: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close writer: %w", err)
	}

	// Upload to API
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(body.Bytes()).
		Post("/platform/storage/resource-store/v1/files/tabular/lookup:upload")

	if err != nil {
		return nil, fmt.Errorf("failed to upload lookup table: %w", err)
	}

	if resp.IsError() {
		return nil, handleUploadError(resp.StatusCode(), resp.String(), req.FilePath)
	}

	var uploadResp UploadResponse
	if err := json.Unmarshal(resp.Body(), &uploadResp); err != nil {
		return nil, fmt.Errorf("failed to parse upload response: %w", err)
	}
	uploadResp.InputRecords = inputRecords
	uploadResp.ParsePattern = req.ParsePattern

	return &uploadResp, nil
}

// Update updates an existing lookup table (same as Create with overwrite=true)
func (h *Handler) Update(path string, req CreateRequest) (*UploadResponse, error) {
	req.FilePath = path
	req.Overwrite = true
	return h.Create(req)
}

// Delete deletes a lookup table
func (h *Handler) Delete(path string) error {
	// Validate path
	if err := ValidatePath(path); err != nil {
		return err
	}

	deleteReq := DeleteRequest{
		FilePath: path,
	}

	resp, err := h.client.HTTP().R().
		SetBody(deleteReq).
		Post("/platform/storage/resource-store/v1/files:delete")

	if err != nil {
		return fmt.Errorf("failed to delete lookup table: %w", err)
	}

	if resp.IsError() {
		return handleDeleteError(resp.StatusCode(), resp.String(), path)
	}

	return nil
}

// Exists checks if a lookup table exists
func (h *Handler) Exists(path string) (bool, error) {
	_, err := h.Get(path)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "doesn't exist") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ValidatePath validates a lookup table file path
func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	if !strings.HasPrefix(path, "/lookups/") {
		return fmt.Errorf("file path must start with /lookups/ (got: %s)", path)
	}

	// Must have at least 2 slashes (check early)
	slashCount := strings.Count(path, "/")
	if slashCount < 2 {
		return fmt.Errorf("file path must contain at least 2 slashes")
	}

	if len(path) > 500 {
		return fmt.Errorf("file path must not exceed 500 characters")
	}

	// Check for valid characters
	for _, c := range path {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '/') {
			return fmt.Errorf("file path contains invalid character: %c", c)
		}
	}

	// Must end with alphanumeric
	lastChar := path[len(path)-1]
	if !((lastChar >= 'a' && lastChar <= 'z') || (lastChar >= 'A' && lastChar <= 'Z') || (lastChar >= '0' && lastChar <= '9')) {
		return fmt.Errorf("file path must end with alphanumeric character")
	}

	return nil
}

// handleUploadError formats upload errors with user-friendly messages
func handleUploadError(statusCode int, body string, path string) error {
	switch statusCode {
	case 400:
		return fmt.Errorf("invalid upload request: %s", body)
	case 403:
		return forbiddenError("write", "storage:files:write", "re-run your create or apply command with --check-scopes", path, body)
	case 409:
		return fmt.Errorf("lookup table %q already exists. Use 'dtctl apply' to update or add --overwrite flag", path)
	case 413:
		return fmt.Errorf("file size exceeds maximum limit (100 MB)")
	default:
		return fmt.Errorf("upload failed (status %d): %s", statusCode, body)
	}
}

// handleDeleteError formats delete errors with user-friendly messages
func handleDeleteError(statusCode int, body string, path string) error {
	switch statusCode {
	case 400:
		return fmt.Errorf("invalid delete request: %s", body)
	case 403:
		return forbiddenError("delete", "storage:files:delete", "dtctl delete lookup "+path+" --check-scopes", path, body)
	case 404:
		return fmt.Errorf("lookup table %q not found. Run 'dtctl get lookups' to list available lookups", path)
	default:
		return fmt.Errorf("delete failed (status %d): %s", statusCode, body)
	}
}

// formatTimestamp formats a timestamp string to a human-readable format
func formatTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}

	// Return relative time (e.g., "2h ago", "1d ago")
	duration := time.Since(t)

	switch {
	case duration < time.Minute:
		return "just now"
	case duration < time.Hour:
		minutes := int(duration.Minutes())
		return fmt.Sprintf("%dm ago", minutes)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		return fmt.Sprintf("%dh ago", hours)
	case duration < 30*24*time.Hour:
		days := int(duration.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	}

	return t.Format("2006-01-02")
}

// parseIntFromString parses an integer from a string, returning an error if parsing fails
func parseIntFromString(s string) (int, error) {
	var val int
	_, err := fmt.Sscanf(s, "%d", &val)
	return val, err
}
