package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func newTestClient(t *testing.T, handler http.Handler) *httpclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return c
}

func TestListSkills(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/davis/copilot/v1/skills", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := SkillsResponse{Skills: []string{"nl2dql", "dql2nl", "document-search"}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills() error: %v", err)
	}
	if len(result.Skills) != 3 {
		t.Errorf("got %d skills, want 3", len(result.Skills))
	}
	if result.Skills[0].Name != "nl2dql" {
		t.Errorf("first skill = %q, want %q", result.Skills[0].Name, "nl2dql")
	}
}

func TestChat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/davis/copilot/v1/skills/conversations:message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := ConversationResponse{
			Text: "Here is the answer.",
			State: &ConversationState{
				Messages: []ConversationMessage{
					{Role: "user", Content: "hello"},
					{Role: "assistant", Content: "Here is the answer."},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Chat(context.Background(), "hello", nil, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if result.Text != "Here is the answer." {
		t.Errorf("Text = %q, want %q", result.Text, "Here is the answer.")
	}
	if result.State == nil || len(result.State.Messages) != 2 {
		t.Errorf("expected 2 state messages, got %v", result.State)
	}
}

func TestNl2Dql(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/davis/copilot/v1/skills/nl2dql:generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Nl2DqlResponse{
			DQL:    "fetch logs | filter status == \"ERROR\"",
			Status: "SUCCESS",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Nl2Dql(context.Background(), "show me error logs")
	if err != nil {
		t.Fatalf("Nl2Dql() error: %v", err)
	}
	if result.Status != "SUCCESS" {
		t.Errorf("Status = %q, want %q", result.Status, "SUCCESS")
	}
	if result.DQL == "" {
		t.Error("DQL should not be empty")
	}
}

func TestChat_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/davis/copilot/v1/skills/conversations:message", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Chat(context.Background(), "hello", nil, nil)
	if err == nil {
		t.Fatal("Chat() expected error for 500")
	}
}
