package notification

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

func TestListEventNotifications(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/notification/v2/event-notifications", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EventNotificationList{
			Results: []EventNotification{
				{ID: "en-1", NotificationType: "EMAIL", Enabled: true},
				{ID: "en-2", NotificationType: "SLACK", Enabled: false},
			},
			Count: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListEventNotifications(context.Background(), "")
	if err != nil {
		t.Fatalf("ListEventNotifications() error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Errorf("got %d notifications, want 2", len(result.Results))
	}
}

func TestGetEventNotification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/notification/v2/event-notifications/en-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EventNotification{ID: "en-1", NotificationType: "EMAIL", Enabled: true}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetEventNotification(context.Background(), "en-1")
	if err != nil {
		t.Fatalf("GetEventNotification() error: %v", err)
	}
	if result.ID != "en-1" {
		t.Errorf("ID = %q, want %q", result.ID, "en-1")
	}
	if !result.Enabled {
		t.Error("Enabled should be true")
	}
}

func TestCreateEventNotification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/notification/v2/event-notifications", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EventNotification{ID: "en-new", NotificationType: "EMAIL", Enabled: true}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	data, _ := json.Marshal(map[string]any{"notificationType": "EMAIL", "enabled": true})
	result, err := h.CreateEventNotification(context.Background(), data)
	if err != nil {
		t.Fatalf("CreateEventNotification() error: %v", err)
	}
	if result.ID != "en-new" {
		t.Errorf("ID = %q, want %q", result.ID, "en-new")
	}
}

func TestDeleteEventNotification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/notification/v2/event-notifications/en-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.DeleteEventNotification(context.Background(), "en-1")
	if err != nil {
		t.Fatalf("DeleteEventNotification() error: %v", err)
	}
}

func TestDeleteEventNotification_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/notification/v2/event-notifications/en-missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.DeleteEventNotification(context.Background(), "en-missing")
	if err == nil {
		t.Fatal("DeleteEventNotification() expected error for 404")
	}
}
