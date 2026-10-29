package copilot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestHandler_ListSkills(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   SkillsResponse
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "list skills",
			statusCode: http.StatusOK,
			response: SkillsResponse{
				Skills: []string{"skill1", "skill2", "skill3"},
			},
			wantErr:   false,
			wantCount: 3,
		},
		{
			name:       "empty skills list",
			statusCode: http.StatusOK,
			response: SkillsResponse{
				Skills: []string{},
			},
			wantErr:   false,
			wantCount: 0,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			response:   SkillsResponse{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/davis/copilot/v1/skills" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			list, err := handler.ListSkills()

			if (err != nil) != tt.wantErr {
				t.Errorf("ListSkills() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if list == nil {
					t.Fatal("ListSkills() returned nil")
				}
				if len(list.Skills) != tt.wantCount {
					t.Errorf("ListSkills() returned %d skills, want %d", len(list.Skills), tt.wantCount)
				}
				// Verify conversion from string array to Skill structs
				if tt.wantCount > 0 && list.Skills[0].Name != tt.response.Skills[0] {
					t.Errorf("Skill name = %q, want %q", list.Skills[0].Name, tt.response.Skills[0])
				}
			}
		})
	}
}

func TestHandler_Chat(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		state      *ConversationState
		context    []ConversationContext
		statusCode int
		response   ConversationResponse
		wantErr    bool
	}{
		{
			name:       "simple chat",
			text:       "Hello CoPilot",
			state:      nil,
			context:    nil,
			statusCode: http.StatusOK,
			response: ConversationResponse{
				Text: "Hello! How can I help you?",
			},
			wantErr: false,
		},
		{
			name: "chat with state",
			text: "Follow up question",
			state: &ConversationState{
				Messages: []ConversationMessage{
					{Role: "user", Content: "Previous message"},
					{Role: "assistant", Content: "Previous response"},
				},
			},
			context:    nil,
			statusCode: http.StatusOK,
			response: ConversationResponse{
				Text: "Follow up response",
			},
			wantErr: false,
		},
		{
			name:  "chat with context",
			text:  "Question with context",
			state: nil,
			context: []ConversationContext{
				{Type: "supplementary", Value: "extra info"},
			},
			statusCode: http.StatusOK,
			response: ConversationResponse{
				Text: "Response with context",
			},
			wantErr: false,
		},
		{
			name:       "server error",
			text:       "test",
			state:      nil,
			context:    nil,
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/davis/copilot/v1/skills/conversations:message" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				// Verify Accept header
				if r.Header.Get("Accept") != "application/json" {
					t.Errorf("Accept header = %q, want application/json", r.Header.Get("Accept"))
				}

				// Verify request body
				var req ConversationRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}
				if req.Text != tt.text {
					t.Errorf("request text = %q, want %q", req.Text, tt.text)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			resp, err := handler.Chat(tt.text, tt.state, tt.context)

			if (err != nil) != tt.wantErr {
				t.Errorf("Chat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if resp == nil {
					t.Fatal("Chat() returned nil")
				}
				if resp.Text != tt.response.Text {
					t.Errorf("Chat() Text = %q, want %q", resp.Text, tt.response.Text)
				}
			}
		})
	}
}

func TestHandler_ChatWithOptions(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		opts       ChatOptions
		statusCode int
		response   ConversationResponse
		wantErr    bool
	}{
		{
			name: "chat with all options",
			text: "Test message",
			opts: ChatOptions{
				Stream:            false,
				DocumentRetrieval: "doc-retrieval-context",
				Supplementary:     "supplementary-context",
				Instruction:       "instruction-context",
			},
			statusCode: http.StatusOK,
			response: ConversationResponse{
				Text: "Response with options",
			},
			wantErr: false,
		},
		{
			name: "chat with minimal options",
			text: "Test message",
			opts: ChatOptions{
				Stream: false,
			},
			statusCode: http.StatusOK,
			response: ConversationResponse{
				Text: "Simple response",
			},
			wantErr: false,
		},
		{
			name: "chat with state",
			text: "Follow up",
			opts: ChatOptions{
				Stream: false,
				State: &ConversationState{
					Messages: []ConversationMessage{
						{Role: "user", Content: "Previous"},
					},
				},
			},
			statusCode: http.StatusOK,
			response: ConversationResponse{
				Text: "Response with state",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Decode to verify context was constructed properly
				var req ConversationRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}

				// Verify context items
				if tt.opts.DocumentRetrieval != "" {
					found := false
					for _, ctx := range req.Context {
						if ctx.Type == "document-retrieval" && ctx.Value == tt.opts.DocumentRetrieval {
							found = true
							break
						}
					}
					if !found {
						t.Error("document-retrieval context not found or incorrect")
					}
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			resp, err := handler.ChatWithOptions(tt.text, tt.opts, nil)

			if (err != nil) != tt.wantErr {
				t.Errorf("ChatWithOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if resp == nil {
					t.Fatal("ChatWithOptions() returned nil")
				}
				if resp.Text != tt.response.Text {
					t.Errorf("ChatWithOptions() Text = %q, want %q", resp.Text, tt.response.Text)
				}
			}
		})
	}
}

func TestHandler_Nl2Dql(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		statusCode int
		response   Nl2DqlResponse
		wantErr    bool
	}{
		{
			name:       "generate DQL query",
			text:       "show me all errors",
			statusCode: http.StatusOK,
			response: Nl2DqlResponse{
				DQL:    "fetch logs | filter status == \"ERROR\"",
				Status: "success",
			},
			wantErr: false,
		},
		{
			name:       "complex query",
			text:       "get CPU usage for last hour",
			statusCode: http.StatusOK,
			response: Nl2DqlResponse{
				DQL:    "timeseries avg(dt.host.cpu.usage), by:{dt.entity.host}",
				Status: "success",
			},
			wantErr: false,
		},
		{
			name:       "server error",
			text:       "test",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/davis/copilot/v1/skills/nl2dql:generate" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				// Verify request body
				var req Nl2DqlRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}
				if req.Text != tt.text {
					t.Errorf("request text = %q, want %q", req.Text, tt.text)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			resp, err := handler.Nl2Dql(tt.text)

			if (err != nil) != tt.wantErr {
				t.Errorf("Nl2Dql() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if resp == nil {
					t.Fatal("Nl2Dql() returned nil")
				}
				if resp.DQL != tt.response.DQL {
					t.Errorf("Nl2Dql() DQL = %q, want %q", resp.DQL, tt.response.DQL)
				}
				if resp.Status != tt.response.Status {
					t.Errorf("Nl2Dql() Status = %q, want %q", resp.Status, tt.response.Status)
				}
			}
		})
	}
}

func TestHandler_Dql2Nl(t *testing.T) {
	tests := []struct {
		name       string
		dql        string
		statusCode int
		response   Dql2NlResponse
		wantErr    bool
	}{
		{
			name:       "explain DQL query",
			dql:        "fetch logs | filter status == \"ERROR\"",
			statusCode: http.StatusOK,
			response: Dql2NlResponse{
				Summary:     "Fetches error logs",
				Explanation: "This query fetches all logs and filters them to show only errors",
				Status:      "success",
			},
			wantErr: false,
		},
		{
			name:       "complex query explanation",
			dql:        "timeseries avg(dt.host.cpu.usage)",
			statusCode: http.StatusOK,
			response: Dql2NlResponse{
				Summary:     "CPU usage over time",
				Explanation: "Shows average CPU usage as a time series",
				Status:      "success",
			},
			wantErr: false,
		},
		{
			name:       "server error",
			dql:        "test",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/davis/copilot/v1/skills/dql2nl:explain" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				// Verify request body
				var req Dql2NlRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}
				if req.DQL != tt.dql {
					t.Errorf("request DQL = %q, want %q", req.DQL, tt.dql)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			resp, err := handler.Dql2Nl(tt.dql)

			if (err != nil) != tt.wantErr {
				t.Errorf("Dql2Nl() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if resp == nil {
					t.Fatal("Dql2Nl() returned nil")
				}
				if resp.Summary != tt.response.Summary {
					t.Errorf("Dql2Nl() Summary = %q, want %q", resp.Summary, tt.response.Summary)
				}
				if resp.Explanation != tt.response.Explanation {
					t.Errorf("Dql2Nl() Explanation = %q, want %q", resp.Explanation, tt.response.Explanation)
				}
			}
		})
	}
}

func TestHandler_DocumentSearch(t *testing.T) {
	tests := []struct {
		name        string
		texts       []string
		collections []string
		exclude     []string
		statusCode  int
		response    DocumentSearchResponse
		wantErr     bool
		wantCount   int
	}{
		{
			name:        "search documents",
			texts:       []string{"kubernetes", "monitoring"},
			collections: []string{"docs"},
			exclude:     nil,
			statusCode:  http.StatusOK,
			response: DocumentSearchResponse{
				Status: "success",
				Results: []ScoredDocument{
					{
						DocumentID:     "doc1",
						RelevanceScore: 0.95,
						DocumentMetadata: DocumentMetadata{
							ID:   "doc1",
							Name: "Kubernetes Guide",
							Type: "guide",
						},
					},
					{
						DocumentID:     "doc2",
						RelevanceScore: 0.85,
						DocumentMetadata: DocumentMetadata{
							ID:   "doc2",
							Name: "Monitoring Best Practices",
							Type: "article",
						},
					},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:        "search with exclusions",
			texts:       []string{"observability"},
			collections: []string{"docs", "tutorials"},
			exclude:     []string{"deprecated"},
			statusCode:  http.StatusOK,
			response: DocumentSearchResponse{
				Status: "success",
				Results: []ScoredDocument{
					{
						DocumentID:     "doc3",
						RelevanceScore: 0.90,
						DocumentMetadata: DocumentMetadata{
							ID:   "doc3",
							Name: "Observability Overview",
							Type: "overview",
						},
					},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:        "no results",
			texts:       []string{"nonexistent"},
			collections: []string{"docs"},
			exclude:     nil,
			statusCode:  http.StatusOK,
			response: DocumentSearchResponse{
				Status:  "success",
				Results: []ScoredDocument{},
			},
			wantErr:   false,
			wantCount: 0,
		},
		{
			name:        "server error",
			texts:       []string{"test"},
			collections: []string{"docs"},
			exclude:     nil,
			statusCode:  http.StatusInternalServerError,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/davis/copilot/v1/skills/document-search:execute" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				// Verify request body
				var req DocumentSearchRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}
				if len(req.Texts) != len(tt.texts) {
					t.Errorf("request texts count = %d, want %d", len(req.Texts), len(tt.texts))
				}
				if len(req.Collections) != len(tt.collections) {
					t.Errorf("request collections count = %d, want %d", len(req.Collections), len(tt.collections))
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			result, err := handler.DocumentSearch(tt.texts, tt.collections, tt.exclude)

			if (err != nil) != tt.wantErr {
				t.Errorf("DocumentSearch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if result == nil {
					t.Fatal("DocumentSearch() returned nil")
				}
				if len(result.Documents) != tt.wantCount {
					t.Errorf("DocumentSearch() returned %d documents, want %d", len(result.Documents), tt.wantCount)
				}
				if result.Status != tt.response.Status {
					t.Errorf("DocumentSearch() Status = %q, want %q", result.Status, tt.response.Status)
				}
				// Verify display fields are populated from metadata
				if tt.wantCount > 0 {
					if result.Documents[0].Name != tt.response.Results[0].DocumentMetadata.Name {
						t.Errorf("Document Name = %q, want %q", result.Documents[0].Name, tt.response.Results[0].DocumentMetadata.Name)
					}
					if result.Documents[0].Type != tt.response.Results[0].DocumentMetadata.Type {
						t.Errorf("Document Type = %q, want %q", result.Documents[0].Type, tt.response.Results[0].DocumentMetadata.Type)
					}
				}
			}
		})
	}
}
