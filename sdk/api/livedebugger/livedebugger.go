package livedebugger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

type Handler struct {
	client     *httpclient.Client
	graphqlURL string
	orgID      string
}

func NewHandler(c *httpclient.Client, environmentURL string) (*Handler, error) {
	graphqlURL, err := buildGraphQLURL(environmentURL)
	if err != nil {
		return nil, err
	}

	orgID, err := extractOrgID(environmentURL)
	if err != nil {
		return nil, err
	}

	return &Handler{client: c, graphqlURL: graphqlURL, orgID: orgID}, nil
}

// NewHandlerWithOrgID creates a handler with an explicitly provided org ID.
// Use this for custom domains or managed environments where the org ID
// cannot be extracted from the URL subdomain.
func NewHandlerWithOrgID(c *httpclient.Client, environmentURL, orgID string) (*Handler, error) {
	graphqlURL, err := buildGraphQLURL(environmentURL)
	if err != nil {
		return nil, err
	}
	return &Handler{client: c, graphqlURL: graphqlURL, orgID: orgID}, nil
}

func (h *Handler) GetOrCreateWorkspace(ctx context.Context, projectPath string) (map[string]interface{}, string, error) {
	query := `query GetOrCreateWorkspaceV2($orgId: ID!, $workspaceInput: WorkspaceGetOrCreateInput) {
  org(id: $orgId) {
    id
    getOrCreateUserWorkspaceV2(workspaceInput: $workspaceInput) {
      id
      orgId
      name
      filterSets {
        labels {
          field
          values
        }
        filters {
          field
          values
        }
      }
      sources
      creationTime
      modificationTime
      creatorEmail
      includeInactive
    }
  }
}`

	variables := map[string]interface{}{
		"orgId": h.orgID,
		"workspaceInput": map[string]interface{}{
			"clientName":  "dtctl",
			"projectPath": projectPath,
		},
	}

	resp, err := h.executeGraphQL(ctx, query, variables)
	if err != nil {
		return nil, "", err
	}

	workspaceID, err := ExtractWorkspaceID(resp)
	if err != nil {
		return resp, "", err
	}

	return resp, workspaceID, nil
}

func (h *Handler) UpdateWorkspaceFilters(ctx context.Context, workspaceID string, filterSets []map[string]interface{}) (map[string]interface{}, error) {
	mutation := `mutation UpdateWorkspaceV2($orgId: ID!, $workspaceId: ID!, $data: WorkspaceInputV2!) {
  org(orgId: $orgId) {
    updateWorkspaceV2(id: $workspaceId, data: $data) {
      id
      orgId
      name
      filterSets {
        labels {
          field
          values
        }
        filters {
          field
          values
        }
      }
      sources
      creationTime
      modificationTime
      creatorEmail
    }
  }
}`

	variables := map[string]interface{}{
		"orgId":       h.orgID,
		"workspaceId": workspaceID,
		"data": map[string]interface{}{
			"filterSets": filterSets,
			"sources":    []interface{}{},
		},
	}

	return h.executeGraphQL(ctx, mutation, variables)
}

func (h *Handler) CreateBreakpoint(ctx context.Context, workspaceID, fileName string, lineNumber int) (map[string]interface{}, error) {
	mutation := `mutation CreateRule($orgId: ID!, $workspaceId: ID!, $ruleData: CreateRuleV2Input!) {
  org(orgId: $orgId) {
    workspace(id: $workspaceId) {
      createRuleV2(data: $ruleData) {
        id
        immutableId
        workspace
        aug {
          mutable_id
          location {
            filename
            sourcePath
            lineno
            sourceRepo
            sha256
            pdbSha256
            line_crc32_2
            line_unique
          }
        }
      }
    }
  }
}`

	variables := map[string]interface{}{
		"orgId":       h.orgID,
		"workspaceId": workspaceID,
		"ruleData": map[string]interface{}{
			"mutableRuleId":           generateMutableRuleID(),
			"lineNumber":              lineNumber,
			"fileName":                fileName,
			"sourceRepo":              "",
			"sourcePath":              fileName,
			"sha256":                  "",
			"lineCrc32_2":             "",
			"lineUnique":              false,
			"pdbSha256":               "",
			"includeExternals":        nil,
			"isDisabled":              false,
			"disableSourceValidation": true,
		},
	}

	return h.executeGraphQL(ctx, mutation, variables)
}

func (h *Handler) GetWorkspaceRules(ctx context.Context, workspaceID string) (map[string]interface{}, error) {
	query := `query GetWorkspaceRules($orgId: ID!, $workspaceId: ID!) {
	org(id: $orgId) {
		id
		workspace(id: $workspaceId) {
			rules {
				id
				immutableId
				template_id
				template_type
				selector
				workspace
				user_email
				workspace_name
				aug_json {
					id
					mutable_id
					location {
						name
						filename
						sourcePath
						sourceRepo
						lineno
						sha256
						includeExternals
						pdbSha256
						line_crc32_2
						line_unique
						role
					}
					action {
						name
						operations
					}
					rateLimit
					conditional
					originalCondition
					globalHitLimit
					globalDisableAfterTime
				}
				is_disabled
				disable_reason
				revision_count
				processing
				indicator {
					indicatorState
					indicatorWarning
				}
			}
		}
	}
}`

	variables := map[string]interface{}{
		"orgId":       h.orgID,
		"workspaceId": workspaceID,
	}

	return h.executeGraphQL(ctx, query, variables)
}

func (h *Handler) DeleteBreakpoint(ctx context.Context, workspaceID, ruleID string) (map[string]interface{}, error) {
	mutation := `mutation DeleteRule($orgId: ID!, $workspaceId: ID!, $ruleId: ID!) {
  org(orgId: $orgId) {
    workspace(id: $workspaceId) {
      deleteRuleV2(mutableId: $ruleId)
    }
  }
}`

	variables := map[string]interface{}{
		"orgId":       h.orgID,
		"workspaceId": workspaceID,
		"ruleId":      ruleID,
	}

	return h.executeGraphQL(ctx, mutation, variables)
}

func (h *Handler) GetRuleStatusBreakdown(ctx context.Context, ruleID string) (map[string]interface{}, error) {
	query := `query GetRuleStatusBreakdown($orgId: ID!, $ruleId: ID!) {
	org(id: $orgId) {
		id
		ruleStatuses(mutableId: $ruleId) {
			ruleId
			status
			rookStatuses {
				rook {
					id
					executable
					hostname
				}
				error {
					message
					type
					parameters
					summary {
						title
						description
						docsLink
						args
					}
				}
				tips {
					description
					docsLink
				}
			}
			agentStatuses {
				controllerId
				error {
					message
					type
					parameters
					summary {
						title
						description
						docsLink
						args
					}
				}
			}
			controllerStatuses {
				controllerId
				error {
					message
					type
					parameters
					summary {
						title
						description
						docsLink
						args
					}
				}
			}
		}
	}
}`

	variables := map[string]interface{}{
		"orgId":  h.orgID,
		"ruleId": ruleID,
	}

	return h.executeGraphQL(ctx, query, variables)
}

func (h *Handler) EditBreakpoint(ctx context.Context, workspaceID string, ruleSettings map[string]interface{}) (map[string]interface{}, error) {
	mutation := `mutation EditRuleV2($orgId: ID!, $workspaceId: ID!, $ruleSettings: EditRuleV2Input!) {
	org(orgId: $orgId) {
		workspace(id: $workspaceId) {
			editRuleV2(data: $ruleSettings) {
				id
				immutableId
				template_id
				template_type
				selector
				workspace
				user_email
				workspace_name
				aug {
					id
					mutable_id
					location {
						name
						filename
						sourcePath
						sourceRepo
						lineno
						sha256
						includeExternals
					}
					action {
						name
						operations
					}
					conditional
					originalCondition
				}
				is_disabled
				disable_reason
				revision_count
				processing
			}
		}
	}
}`

	variables := map[string]interface{}{
		"orgId":        h.orgID,
		"workspaceId":  workspaceID,
		"ruleSettings": ruleSettings,
	}

	return h.executeGraphQL(ctx, mutation, variables)
}

func (h *Handler) EnableOrDisableBreakpoints(ctx context.Context, workspaceID string, ruleIDs []string, isDisabled bool) (map[string]interface{}, error) {
	mutation := `mutation EnableOrDisableRules($orgId: ID!, $workspaceId: ID!, $rulesIds: [String]!, $isDisabled: Boolean!) {
	org(orgId: $orgId) {
		workspace(id: $workspaceId) {
			enableOrDisableRules(isDisabled: $isDisabled, rulesIds: $rulesIds) {
				id
				immutableId
				is_disabled
			}
		}
	}
}`

	variables := map[string]interface{}{
		"orgId":       h.orgID,
		"workspaceId": workspaceID,
		"rulesIds":    ruleIDs,
		"isDisabled":  isDisabled,
	}

	return h.executeGraphQL(ctx, mutation, variables)
}

func (h *Handler) DeleteAllBreakpoints(ctx context.Context, workspaceID string) (map[string]interface{}, error) {
	mutation := `mutation DeleteAllRulesFromWorkspace($orgId: ID!, $workspaceId: ID!, $data: DeleteWorkspaceRulesInput!) {
  org(orgId: $orgId) {
    workspace(id: $workspaceId) {
      deleteAllRulesFromWorkspaceV2(data: $data)
    }
  }
}`

	variables := map[string]interface{}{
		"orgId":       h.orgID,
		"workspaceId": workspaceID,
		"data": map[string]interface{}{
			"ruleType": "DumpFrame",
		},
	}

	return h.executeGraphQL(ctx, mutation, variables)
}

func BuildFilterSets(filters map[string][]string) []map[string]interface{} {
	if len(filters) == 0 {
		return []map[string]interface{}{}
	}

	labelList := make([]map[string]interface{}, 0, len(filters))
	for field, values := range filters {
		labelList = append(labelList, map[string]interface{}{
			"field":  field,
			"values": values,
		})
	}

	return []map[string]interface{}{
		{
			"filters": []interface{}{},
			"labels":  labelList,
		},
	}
}

func (h *Handler) executeGraphQL(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	requestBody := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("dt-external-source", "dtctl").
		SetBody(requestBody).
		Post(h.graphqlURL)
	if err != nil {
		return nil, fmt.Errorf("call live debugger graphql: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("live debugger graphql request: %w", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &response); err != nil {
		return nil, fmt.Errorf("live debugger graphql: parse response: %w", err)
	}

	if errorsValue, ok := response["errors"]; ok {
		return response, fmt.Errorf("live debugger graphql returned errors: %s", extractGraphQLErrorMessages(errorsValue))
	}

	return response, nil
}

func extractGraphQLErrorMessages(errorsValue interface{}) string {
	errs, ok := errorsValue.([]interface{})
	if !ok {
		return fmt.Sprintf("%v", errorsValue)
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		if m, ok := e.(map[string]interface{}); ok {
			if msg, ok := m["message"].(string); ok {
				msgs = append(msgs, msg)
				continue
			}
		}
		msgs = append(msgs, fmt.Sprintf("%v", e))
	}
	return strings.Join(msgs, "; ")
}

func buildGraphQLURL(environmentURL string) (string, error) {
	envURL := strings.TrimSpace(environmentURL)
	if envURL == "" {
		return "", fmt.Errorf("context environment is required")
	}

	parsed, err := url.Parse(envURL)
	if err != nil {
		return "", fmt.Errorf("invalid context environment URL %q: %w", environmentURL, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("context environment must be a full URL, got %q", environmentURL)
	}

	return strings.TrimRight(parsed.String(), "/") + "/platform/dob/graphql", nil
}

// extractOrgID extracts the organization ID from a Dynatrace environment URL.
// It assumes the standard SaaS URL format where the first subdomain is the org ID
// (e.g., "abc12345.apps.dynatrace.com"). For custom domains or managed
// environments, callers should use NewHandlerWithOrgID instead.
func extractOrgID(environmentURL string) (string, error) {
	return httpclient.ExtractSubdomain(environmentURL)
}

func generateMutableRuleID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read practically never fails on modern systems,
		// but if it does, use a timestamp-based fallback to avoid collisions.
		return "dtctl-rule-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "dtctl-rule-" + hex.EncodeToString(buf)
}
