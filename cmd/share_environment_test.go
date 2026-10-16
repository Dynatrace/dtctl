package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// envShareServer mocks the Document API endpoints share/unshare --environment
// touch, for a launchpad document "lp-1" that starts out public with one
// read environment share. Every mutating request is recorded as
// "METHOD path [detail]" in call order.
type envShareServer struct {
	mu           sync.Mutex
	mutations    []string
	isPrivate    bool
	deleteStatus int // non-zero: deleting an environment share fails with this status
	shares       []map[string]any
}

func newEnvShareServer(t *testing.T) (*envShareServer, *httptest.Server) {
	t.Helper()
	s := &envShareServer{
		shares: []map[string]any{{"id": "env-share-1", "documentId": "lp-1", "access": []string{"read"}}},
	}
	record := func(entry string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.mutations = append(s.mutations, entry)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/lp-1/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "lp-1", "name": "Team launchpad", "type": "launchpad",
			"owner": "owner-1", "version": 4, "isPrivate": s.isPrivate,
		})
	})
	for action, private := range map[string]bool{"makePublic": false, "makePrivate": true} {
		mux.HandleFunc("/platform/document/v1/documents/lp-1:"+action, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			record("POST " + action)
			s.mu.Lock()
			s.isPrivate = private
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"environment-shares": s.shares, "totalCount": len(s.shares)})
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		record("POST environment-share " + body["documentId"] + " " + body["access"])
		access := []string{"read"}
		if body["access"] == "read-write" {
			access = []string{"read", "write"}
		}
		// One share per access level, as the API enforces.
		for _, existing := range s.shares {
			if strings.Join(existing["access"].([]string), ",") == strings.Join(access, ",") {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":409,"message":"Share already exists"}}`))
				return
			}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "env-share-2", "documentId": body["documentId"], "access": access})
	})
	mux.HandleFunc("/platform/document/v1/environment-shares/", func(w http.ResponseWriter, r *http.Request) {
		record(r.Method + " " + r.URL.Path)
		if s.deleteStatus != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.deleteStatus)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Forbidden"}}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/platform/document/v1/direct-shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"direct-shares": []map[string]any{{"id": "direct-1", "documentId": "lp-1", "access": []string{"read"}}},
				"totalCount":    1,
			})
			return
		}
		record(r.Method + " " + r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/platform/document/v1/direct-shares/", func(w http.ResponseWriter, r *http.Request) {
		record(r.Method + " " + r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *envShareServer) calls() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.mutations, "\n")
}

// runShareCmd runs c on document "lp-1" with flags set, and returns what it
// printed on stdout and stderr.
func runShareCmd(t *testing.T, c *cobra.Command, flags map[string]string) (stdout, stderr string, err error) {
	t.Helper()
	for k, v := range flags {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
	}
	stdout = captureExtStdout(t, func() {
		stderr = captureDocStderr(t, func() {
			err = c.RunE(c, []string{"lp-1"})
		})
	})
	return stdout, stderr, err
}

func assertCalls(t *testing.T, s *envShareServer, want string) {
	t.Helper()
	if got := s.calls(); got != want {
		t.Errorf("mutating calls:\n%s\nwant:\n%s", got, want)
	}
}

// TestShareDocument_EnvironmentLink: --environment link creates an environment
// share for a document of any type (here a launchpad), prints the link that
// hands it out, and leaves the document's visibility alone.
func TestShareDocument_EnvironmentLink(t *testing.T) {
	s, srv := newEnvShareServer(t)
	s.isPrivate = true
	s.shares = nil
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	stdout, _, err := runShareCmd(t, shareDocumentCmd, map[string]string{"environment": "link", "access": "read-write"})
	if err != nil {
		t.Fatalf("share document --environment link: %v", err)
	}
	assertCalls(t, s, "POST environment-share lp-1 read-write")
	if want := srv.URL + "/ui/document/v0/#share=env-share-2\n"; stdout != want {
		t.Errorf("stdout = %q, want the share link %q", stdout, want)
	}
}

// TestShareDocument_EnvironmentLinkAgent: agent mode returns the share ID and
// the link as the result.
func TestShareDocument_EnvironmentLinkAgent(t *testing.T) {
	s, srv := newEnvShareServer(t)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)
	withAgentMode(t, true)

	stdout, _, err := runShareCmd(t, shareDocumentCmd, map[string]string{"environment": "link"})
	if err != nil {
		t.Fatalf("share document --environment link: %v", err)
	}
	assertCalls(t, s, "")
	var env struct {
		OK     bool                   `json:"ok"`
		Result environmentShareResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("agent output is not an envelope: %v\n%s", err, stdout)
	}
	want := environmentLinkResult{
		ShareID: "env-share-1", Access: "read",
		URL: srv.URL + "/ui/document/v0/#share=env-share-1",
	}
	if !env.OK || env.Result.DocumentID != "lp-1" || env.Result.Link == nil || *env.Result.Link != want ||
		env.Result.Visibility != nil {
		t.Errorf("result = %+v (ok %v), want link %+v and no visibility", env.Result, env.OK, want)
	}
}

// TestShareDocument_EnvironmentLinkAtAnotherLevel: a document has at most
// one environment share per access level, so a link at another level is
// neither reused (that would hand out read-write to someone who asked for
// read) nor replaced (that would break the links already handed out): the
// requested level is created next to it. The existing level is reused.
func TestShareDocument_EnvironmentLinkAtAnotherLevel(t *testing.T) {
	readWrite := []map[string]any{{"id": "env-share-1", "documentId": "lp-1", "access": []string{"read", "write"}}}

	t.Run("another level is created next to it", func(t *testing.T) {
		s, srv := newEnvShareServer(t)
		s.shares = readWrite
		setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

		stdout, _, err := runShareCmd(t, shareDocumentCmd, map[string]string{"environment": "link"})
		if err != nil {
			t.Fatalf("share document --environment link: %v", err)
		}
		assertCalls(t, s, "POST environment-share lp-1 read")
		if want := srv.URL + "/ui/document/v0/#share=env-share-2\n"; stdout != want {
			t.Errorf("stdout = %q, want the new read link %q", stdout, want)
		}
	})

	t.Run("the same level is reused", func(t *testing.T) {
		s, srv := newEnvShareServer(t)
		s.shares = readWrite
		setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

		stdout, _, err := runShareCmd(t, shareDocumentCmd, map[string]string{"environment": "link", "access": "read-write"})
		if err != nil {
			t.Fatalf("share document --environment link --access read-write: %v", err)
		}
		assertCalls(t, s, "")
		if !strings.Contains(stdout, "#share=env-share-1") {
			t.Errorf("stdout %q does not carry the existing link", stdout)
		}
	})
}

// TestShareDocument_EnvironmentPublic: --environment public only marks the
// document public; no environment share is created.
func TestShareDocument_EnvironmentPublic(t *testing.T) {
	s, srv := newEnvShareServer(t)
	s.isPrivate = true
	s.shares = nil
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	if _, _, err := runShareCmd(t, shareDocumentCmd, map[string]string{"environment": "public"}); err != nil {
		t.Fatalf("share document --environment public: %v", err)
	}
	assertCalls(t, s, "POST makePublic")
}

// TestShareDocument_EnvironmentLinkAndPublic: both modes at once create the
// link first, then make the document public, and stdout still carries
// nothing but the link.
func TestShareDocument_EnvironmentLinkAndPublic(t *testing.T) {
	s, srv := newEnvShareServer(t)
	s.isPrivate = true
	s.shares = nil
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	stdout, _, err := runShareCmd(t, shareDocumentCmd, map[string]string{"environment": "link,public"})
	if err != nil {
		t.Fatalf("share document --environment link,public: %v", err)
	}
	assertCalls(t, s, "POST environment-share lp-1 read\nPOST makePublic")
	if want := srv.URL + "/ui/document/v0/#share=env-share-2\n"; stdout != want {
		t.Errorf("stdout = %q, want only the share link %q", stdout, want)
	}
}

func TestShareDocument_EnvironmentRejectedInvocations(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		want  string
	}{
		{"unknown mode", map[string]string{"environment": "everyone"}, "must be 'link', 'public', or both"},
		{"unknown mode in a list", map[string]string{"environment": "link,everyone"}, "must be 'link', 'public', or both"},
		{"empty mode", map[string]string{"environment": ""}, "needs a value"},
		{"with --user", map[string]string{"environment": "link", "user": "user-1"}, "cannot be combined"},
		{"with --group", map[string]string{"environment": "link", "group": "group-1"}, "cannot be combined"},
		{"with --no-notify", map[string]string{"environment": "link", "no-notify": "true"}, "--no-notify"},
		{"public with --access", map[string]string{"environment": "public", "access": "read-write"}, "--access does not apply"},
		{"public with --access read", map[string]string{"environment": "public", "access": "read"}, "--access does not apply"},
		{"nothing to share with", map[string]string{}, "--environment link,public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, srv := newEnvShareServer(t)
			setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

			_, _, err := runShareCmd(t, shareDocumentCmd, tt.flags)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.want)
			}
			assertCalls(t, s, "")
		})
	}
}

// TestShareUnshareDocument_EnvironmentNeedsSafetyLevel: environment sharing
// changes who can read the document, so a readonly context refuses it.
func TestShareUnshareDocument_EnvironmentNeedsSafetyLevel(t *testing.T) {
	for _, c := range []*cobra.Command{shareDocumentCmd, unshareDocumentCmd} {
		for _, mode := range []string{"link", "public", "link,public"} {
			t.Run(c.Parent().Name()+" "+mode, func(t *testing.T) {
				s, srv := newEnvShareServer(t)
				setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadOnly)

				_, _, err := runShareCmd(t, c, map[string]string{"environment": mode})
				if err == nil || !strings.Contains(err.Error(), "does not allow") {
					t.Fatalf("error = %v, want the safety check's refusal", err)
				}
				assertCalls(t, s, "")
			})
		}
	}
}

func TestShareUnshareDocument_EnvironmentDryRun(t *testing.T) {
	for _, c := range []*cobra.Command{shareDocumentCmd, unshareDocumentCmd} {
		for _, mode := range []string{"link", "public", "link,public"} {
			t.Run(c.Parent().Name()+" "+mode, func(t *testing.T) {
				s, srv := newEnvShareServer(t)
				setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)
				gFlags.dryRun = true

				stdout, _, err := runShareCmd(t, c, map[string]string{"environment": mode})
				if err != nil {
					t.Fatalf("dry run: %v", err)
				}
				if !strings.Contains(stdout, "Dry run") {
					t.Errorf("output %q is not a dry-run preview", stdout)
				}
				assertCalls(t, s, "")
			})
		}
	}
}

// TestUnshareDocument_EnvironmentLink: --environment link deletes the
// environment share and leaves the document's visibility and direct shares
// alone.
func TestUnshareDocument_EnvironmentLink(t *testing.T) {
	s, srv := newEnvShareServer(t)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	if _, _, err := runShareCmd(t, unshareDocumentCmd, map[string]string{"environment": "link"}); err != nil {
		t.Fatalf("unshare document --environment link: %v", err)
	}
	assertCalls(t, s, "DELETE /platform/document/v1/environment-shares/env-share-1")
}

// TestUnshareDocument_EnvironmentLinkAccessFilter: with --access only shares at
// exactly that level are deleted, and a filter that matches nothing changes
// nothing and says so.
func TestUnshareDocument_EnvironmentLinkAccessFilter(t *testing.T) {
	s, srv := newEnvShareServer(t)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	stdout, _, err := runShareCmd(t, unshareDocumentCmd, map[string]string{"environment": "link", "access": "read-write"})
	if err != nil {
		t.Fatalf("unshare document --environment link --access read-write: %v", err)
	}
	assertCalls(t, s, "")
	if !strings.Contains(stdout, "No read-write environment share") || !strings.Contains(stdout, "nothing changed") {
		t.Errorf("stdout %q does not say that nothing changed", stdout)
	}
}

// TestUnshareDocument_EnvironmentLinkForbidden: an OAuth session from before
// dtctl requested document:environment-shares:delete gets a 403; the error
// says how to get the scope.
func TestUnshareDocument_EnvironmentLinkForbidden(t *testing.T) {
	s, srv := newEnvShareServer(t)
	s.deleteStatus = http.StatusForbidden
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	_, _, err := runShareCmd(t, unshareDocumentCmd, map[string]string{"environment": "link"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"document:environment-shares:delete", "dtctl auth login", "--check-scopes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestUnshareDocument_EnvironmentPublic: --environment public only marks the
// document private again; environment shares stay.
func TestUnshareDocument_EnvironmentPublic(t *testing.T) {
	s, srv := newEnvShareServer(t)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	if _, _, err := runShareCmd(t, unshareDocumentCmd, map[string]string{"environment": "public"}); err != nil {
		t.Fatalf("unshare document --environment public: %v", err)
	}
	assertCalls(t, s, "POST makePrivate")
}

// TestUnshareDocument_EnvironmentLinkAndPublic: both modes at once make the
// document private first, then delete the link, so a failure partway leaves
// the narrower exposure.
func TestUnshareDocument_EnvironmentLinkAndPublic(t *testing.T) {
	s, srv := newEnvShareServer(t)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	if _, _, err := runShareCmd(t, unshareDocumentCmd, map[string]string{"environment": "link,public"}); err != nil {
		t.Fatalf("unshare document --environment link,public: %v", err)
	}
	assertCalls(t, s, "POST makePrivate\nDELETE /platform/document/v1/environment-shares/env-share-1")
}

func TestUnshareDocument_EnvironmentRejectedInvocations(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		want  string
	}{
		// An unrecognized level used to be matched as 'read', so a typo deleted read shares.
		{"unknown access", map[string]string{"environment": "link", "access": "rw"}, "invalid access level"},
		{"unknown mode", map[string]string{"environment": "everyone"}, "must be 'link', 'public', or both"},
		{"public with --access", map[string]string{"environment": "public", "access": "read"}, "--access does not apply"},
		{"nothing to remove", map[string]string{}, "--environment link,public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, srv := newEnvShareServer(t)
			setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

			_, _, err := runShareCmd(t, unshareDocumentCmd, tt.flags)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.want)
			}
			assertCalls(t, s, "")
		})
	}
}

// TestUnshareDocument_AllLeavesEnvironment: --all keeps its meaning (user and
// group shares only); combined with --environment it removes both.
func TestUnshareDocument_AllLeavesEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		want  string
	}{
		{"--all", map[string]string{"all": "true"},
			"DELETE /platform/document/v1/direct-shares/direct-1"},
		{"--all --environment link", map[string]string{"all": "true", "environment": "link"},
			"DELETE /platform/document/v1/environment-shares/env-share-1\n" +
				"DELETE /platform/document/v1/direct-shares/direct-1"},
		// --access filters the direct shares here, so it is not rejected.
		{"--all --access read --environment public", map[string]string{"all": "true", "access": "read", "environment": "public"},
			"POST makePrivate\nDELETE /platform/document/v1/direct-shares/direct-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, srv := newEnvShareServer(t)
			setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

			if _, _, err := runShareCmd(t, unshareDocumentCmd, tt.flags); err != nil {
				t.Fatalf("unshare document: %v", err)
			}
			assertCalls(t, s, tt.want)
		})
	}
}
