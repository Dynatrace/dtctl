package document

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// TestTrashHandler_List tests listing trashed documents
func TestTrashHandler_List(t *testing.T) {
	deletedTime := time.Now().Add(-24 * time.Hour)

	tests := []struct {
		name          string
		opts          TrashListOptions
		mockResponse  TrashList
		expectedCount int
		wantErr       bool
	}{
		{
			name: "list all",
			opts: TrashListOptions{},
			mockResponse: TrashList{
				Documents: []TrashDocumentListEntry{
					{
						ID:   "doc1",
						Type: "dashboard",
						Name: "Dashboard 1",
						DeletionInfo: DeletionInfo{
							DeletedBy:   "user@example.com",
							DeletedTime: deletedTime,
						},
					},
					{
						ID:   "doc2",
						Type: "notebook",
						Name: "Notebook 1",
						DeletionInfo: DeletionInfo{
							DeletedBy:   "admin@example.com",
							DeletedTime: deletedTime,
						},
					},
				},
				TotalCount: 2,
			},
			expectedCount: 2,
			wantErr:       false,
		},
		{
			name: "filter by type",
			opts: TrashListOptions{Type: "dashboard"},
			mockResponse: TrashList{
				Documents: []TrashDocumentListEntry{
					{
						ID:   "doc1",
						Type: "dashboard",
						Name: "Dashboard 1",
						DeletionInfo: DeletionInfo{
							DeletedBy:   "user@example.com",
							DeletedTime: deletedTime,
						},
					},
				},
				TotalCount: 1,
			},
			expectedCount: 1,
			wantErr:       false,
		},
		{
			name: "empty trash",
			opts: TrashListOptions{},
			mockResponse: TrashList{
				Documents:  []TrashDocumentListEntry{},
				TotalCount: 0,
			},
			expectedCount: 0,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/document/v1/trash/documents" {
					t.Errorf("Expected path /platform/document/v1/trash/documents, got %s", r.URL.Path)
				}

				// Document API accepts page-size with page-key (no constraint to simulate here)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(tt.mockResponse)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewTrashHandler(c)
			docs, err := handler.List(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("List() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(docs) != tt.expectedCount {
				t.Errorf("List() got %d documents, want %d", len(docs), tt.expectedCount)
			}

			// Verify computed fields are populated
			for _, doc := range docs {
				if doc.DeletedBy == "" {
					t.Error("DeletedBy should be set")
				}
				if doc.DeletedAt.IsZero() {
					t.Error("DeletedAt should be set")
				}
			}
		})
	}
}

// TestTrashHandler_List_Pagination tests paginated listing of trashed documents
func TestTrashHandler_List_Pagination(t *testing.T) {
	deletedTime := time.Now().Add(-24 * time.Hour)
	pageIndex := 0
	pages := []TrashList{
		{
			Documents: []TrashDocumentListEntry{
				{
					ID:   "doc1",
					Type: "dashboard",
					Name: "Dashboard 1",
					DeletionInfo: DeletionInfo{
						DeletedBy:   "user@example.invalid",
						DeletedTime: deletedTime,
					},
				},
				{
					ID:   "doc2",
					Type: "notebook",
					Name: "Notebook 1",
					DeletionInfo: DeletionInfo{
						DeletedBy:   "admin@example.invalid",
						DeletedTime: deletedTime,
					},
				},
			},
			TotalCount:  3,
			NextPageKey: "page2",
		},
		{
			Documents: []TrashDocumentListEntry{
				{
					ID:   "doc3",
					Type: "dashboard",
					Name: "Dashboard 2",
					DeletionInfo: DeletionInfo{
						DeletedBy:   "user@example.invalid",
						DeletedTime: deletedTime,
					},
				},
			},
			TotalCount: 3,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/document/v1/trash/documents" {
			t.Errorf("Expected path /platform/document/v1/trash/documents, got %s", r.URL.Path)
		}

		// Document API accepts page-size with page-key (no constraint to simulate here)

		// Verify filter is sent on every request (page tokens do NOT preserve it)
		if r.URL.Query().Get("filter") == "" {
			t.Error("filter must be sent on every request, including subsequent pages")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if pageIndex >= len(pages) {
			t.Error("received more requests than expected pages")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pages[pageIndex])
		pageIndex++
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)

	handler := NewTrashHandler(c)
	docs, err := handler.List(TrashListOptions{ChunkSize: 10, Type: "dashboard"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(docs) != 3 {
		t.Errorf("expected 3 documents across pages, got %d", len(docs))
	}
}

// TestTrashHandler_Get tests getting a specific trashed document
func TestTrashHandler_Get(t *testing.T) {
	docID := "test-doc-123"
	deletedTime := time.Now().Add(-5 * 24 * time.Hour)

	mockDoc := TrashedDocument{
		ID:      docID,
		Type:    "dashboard",
		Name:    "Test Dashboard",
		Version: 1,
		Owner:   "user1",
		DeletionInfo: DeletionInfo{
			DeletedBy:   "user1",
			DeletedTime: deletedTime,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/platform/document/v1/trash/documents/" + docID
		if r.URL.Path != expectedPath {
			t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockDoc)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)

	handler := NewTrashHandler(c)
	doc, err := handler.Get(docID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if doc.ID != docID {
		t.Errorf("Get() ID = %s, want %s", doc.ID, docID)
	}

	// Verify computed fields are set
	if doc.DeletedBy == "" {
		t.Error("DeletedBy should be set")
	}

	if doc.DeletedAt.IsZero() {
		t.Error("DeletedAt should be set")
	}
}

// TestTrashHandler_Get_NotFound tests error handling for non-existent document
func TestTrashHandler_Get_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)

	handler := NewTrashHandler(c)
	_, err = handler.Get("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent document, got nil")
	}
}

// TestTrashHandler_Restore tests restoring a document
func TestTrashHandler_Restore(t *testing.T) {
	tests := []struct {
		name        string
		docID       string
		opts        RestoreOptions
		statusCode  int
		wantErr     bool
		errContains string
	}{
		{
			name:       "successful restore",
			docID:      "doc123",
			opts:       RestoreOptions{},
			statusCode: 200,
			wantErr:    false,
		},
		{
			name:       "restore with force",
			docID:      "doc123",
			opts:       RestoreOptions{Force: true},
			statusCode: 200,
			wantErr:    false,
		},
		{
			name:        "name conflict",
			docID:       "doc123",
			opts:        RestoreOptions{},
			statusCode:  409,
			wantErr:     true,
			errContains: "name conflict",
		},
		{
			name:        "not found",
			docID:       "nonexistent",
			opts:        RestoreOptions{},
			statusCode:  404,
			wantErr:     true,
			errContains: "Not Found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// For POST restore request
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(map[string]string{})
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewTrashHandler(c)
			err = handler.Restore(tt.docID, tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("Restore() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Restore() error = %v, should contain %s", err, tt.errContains)
				}
			}
		})
	}
}

// TestTrashHandler_Delete tests permanently deleting a document
func TestTrashHandler_Delete(t *testing.T) {
	tests := []struct {
		name       string
		docID      string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "successful delete",
			docID:      "doc123",
			statusCode: 204,
			wantErr:    false,
		},
		{
			name:       "not found",
			docID:      "nonexistent",
			statusCode: 404,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(map[string]string{})
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewTrashHandler(c)
			err = handler.Delete(tt.docID)
			if (err != nil) != tt.wantErr {
				t.Errorf("Delete() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
