package copilot

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcop "github.com/Dynatrace/dtctl/sdk/api/copilot"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-export SDK types that don't need table tags as aliases.
type (
	SkillsResponse        = sdkcop.SkillsResponse
	ConversationRequest   = sdkcop.ConversationRequest
	ConversationState     = sdkcop.ConversationState
	ConversationMessage   = sdkcop.ConversationMessage
	ConversationContext   = sdkcop.ConversationContext
	StreamChunk           = sdkcop.StreamChunk
	StreamChunkData       = sdkcop.StreamChunkData
	ChatOptions           = sdkcop.ChatOptions
	Nl2DqlRequest         = sdkcop.Nl2DqlRequest
	Dql2NlRequest         = sdkcop.Dql2NlRequest
	DocumentSearchRequest = sdkcop.DocumentSearchRequest
	DocumentMetadata      = sdkcop.DocumentMetadata
)

// Skill represents an available CoPilot skill with CLI display fields.
type Skill struct {
	Name string `table:"NAME"`
}

// SkillList is the processed list for display.
type SkillList struct {
	Skills []Skill
}

// ConversationResponse represents a response from the CoPilot conversation endpoint.
type ConversationResponse struct {
	Text  string             `json:"text" table:"RESPONSE"`
	State *ConversationState `json:"state,omitempty" table:"-"`
}

// Nl2DqlResponse represents the response from the NL to DQL skill.
type Nl2DqlResponse struct {
	DQL          string `json:"dql" table:"DQL"`
	MessageToken string `json:"messageToken" table:"-"`
	Status       string `json:"status" table:"STATUS"`
}

// Dql2NlResponse represents the response from the DQL to NL skill.
type Dql2NlResponse struct {
	Summary      string `json:"summary" table:"SUMMARY"`
	Explanation  string `json:"explanation" table:"EXPLANATION"`
	MessageToken string `json:"messageToken" table:"-"`
	Status       string `json:"status" table:"STATUS"`
}

// ScoredDocument represents a document with its relevance score.
type ScoredDocument struct {
	DocumentID       string           `json:"documentId" table:"ID"`
	RelevanceScore   float64          `json:"relevanceScore" table:"SCORE"`
	DocumentMetadata DocumentMetadata `json:"documentMetadata" table:"-"`
	Name             string           `table:"NAME"`
	Type             string           `table:"TYPE"`
}

// DocumentSearchResponse represents the response from document search.
// Used for JSON deserialization in tests.
type DocumentSearchResponse struct {
	MessageToken string           `json:"messageToken"`
	Results      []ScoredDocument `json:"results"`
	Status       string           `json:"status"`
}

// DocumentSearchResult is a processed result for display.
type DocumentSearchResult struct {
	Documents []ScoredDocument
	Status    string
}

// fromSDKConversationResponse converts an SDK ConversationResponse to the CLI type.
func fromSDKConversationResponse(s *sdkcop.ConversationResponse) *ConversationResponse {
	return &ConversationResponse{
		Text:  s.Text,
		State: s.State,
	}
}

// fromSDKNl2DqlResponse converts an SDK Nl2DqlResponse to the CLI type.
func fromSDKNl2DqlResponse(s *sdkcop.Nl2DqlResponse) *Nl2DqlResponse {
	return &Nl2DqlResponse{
		DQL:          s.DQL,
		MessageToken: s.MessageToken,
		Status:       s.Status,
	}
}

// fromSDKDql2NlResponse converts an SDK Dql2NlResponse to the CLI type.
func fromSDKDql2NlResponse(s *sdkcop.Dql2NlResponse) *Dql2NlResponse {
	return &Dql2NlResponse{
		Summary:      s.Summary,
		Explanation:  s.Explanation,
		MessageToken: s.MessageToken,
		Status:       s.Status,
	}
}

// fromSDKScoredDocument converts an SDK ScoredDocument to the CLI type.
func fromSDKScoredDocument(s *sdkcop.ScoredDocument) ScoredDocument {
	return ScoredDocument{
		DocumentID:       s.DocumentID,
		RelevanceScore:   s.RelevanceScore,
		DocumentMetadata: s.DocumentMetadata,
		Name:             s.DocumentMetadata.Name,
		Type:             s.DocumentMetadata.Type,
	}
}

// fromSDKSkillList converts an SDK SkillList to the CLI type.
func fromSDKSkillList(s *sdkcop.SkillList) *SkillList {
	skills := make([]Skill, len(s.Skills))
	for i, sk := range s.Skills {
		skills[i] = Skill{Name: sk.Name}
	}
	return &SkillList{Skills: skills}
}

// Handler handles Davis CoPilot resources.
type Handler struct {
	sdk *sdkcop.Handler
}

// NewHandler creates a new copilot handler
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkcop.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// ListSkills retrieves all available CoPilot skills
func (h *Handler) ListSkills() (*SkillList, error) {
	sdkResult, err := h.sdk.ListSkills(context.Background())
	if err != nil {
		return nil, err
	}
	return fromSDKSkillList(sdkResult), nil
}

// Chat sends a message to CoPilot and returns the response
func (h *Handler) Chat(text string, state *ConversationState, ctx []ConversationContext) (*ConversationResponse, error) {
	sdkResult, err := h.sdk.Chat(context.Background(), text, state, ctx)
	if err != nil {
		return nil, err
	}
	return fromSDKConversationResponse(sdkResult), nil
}

// ChatStream sends a message to CoPilot and streams the response
func (h *Handler) ChatStream(text string, state *ConversationState, ctx []ConversationContext, callback func(chunk StreamChunk) error) (*ConversationResponse, error) {
	sdkResult, err := h.sdk.ChatStream(context.Background(), text, state, ctx, callback)
	if err != nil {
		return nil, err
	}
	return fromSDKConversationResponse(sdkResult), nil
}

// ChatWithOptions sends a message with options
func (h *Handler) ChatWithOptions(text string, opts ChatOptions, streamCallback func(chunk StreamChunk) error) (*ConversationResponse, error) {
	sdkResult, err := h.sdk.ChatWithOptions(context.Background(), text, opts, streamCallback)
	if err != nil {
		return nil, err
	}
	return fromSDKConversationResponse(sdkResult), nil
}

// Nl2Dql converts natural language to a DQL query
func (h *Handler) Nl2Dql(text string) (*Nl2DqlResponse, error) {
	sdkResult, err := h.sdk.Nl2Dql(context.Background(), text)
	if err != nil {
		return nil, err
	}
	return fromSDKNl2DqlResponse(sdkResult), nil
}

// Dql2Nl explains a DQL query in natural language
func (h *Handler) Dql2Nl(dql string) (*Dql2NlResponse, error) {
	sdkResult, err := h.sdk.Dql2Nl(context.Background(), dql)
	if err != nil {
		return nil, err
	}
	return fromSDKDql2NlResponse(sdkResult), nil
}

// DocumentSearch searches for relevant documents
func (h *Handler) DocumentSearch(texts []string, collections []string, exclude []string) (*DocumentSearchResult, error) {
	sdkResult, err := h.sdk.DocumentSearch(context.Background(), texts, collections, exclude)
	if err != nil {
		return nil, err
	}
	docs := make([]ScoredDocument, len(sdkResult.Documents))
	for i, d := range sdkResult.Documents {
		docs[i] = fromSDKScoredDocument(&d)
	}
	return &DocumentSearchResult{
		Documents: docs,
		Status:    sdkResult.Status,
	}, nil
}
