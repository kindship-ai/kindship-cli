package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
)

func TestDefaultSiteWorkspaceDirContainerMode(t *testing.T) {
	ctx := &auth.Context{Method: auth.AuthMethodServiceKey}

	dir, err := defaultSiteWorkspaceDir(ctx, "demo-site")
	if err != nil {
		t.Fatalf("defaultSiteWorkspaceDir returned error: %v", err)
	}

	if want := "/workspace/sites/demo-site"; dir != want {
		t.Fatalf("expected %q, got %q", want, dir)
	}
}

func TestDefaultSiteWorkspaceDirLocalModeUsesHome(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	ctx := &auth.Context{Method: auth.AuthMethodOAuth}

	dir, err := defaultSiteWorkspaceDir(ctx, "demo-site")
	if err != nil {
		t.Fatalf("defaultSiteWorkspaceDir returned error: %v", err)
	}

	if want := filepath.Join(homeDir, "kindship", "sites", "demo-site"); dir != want {
		t.Fatalf("expected %q, got %q", want, dir)
	}
}

func TestDefaultSiteWorkspaceDirRejectsInvalidSiteName(t *testing.T) {
	ctx := &auth.Context{Method: auth.AuthMethodOAuth}

	if _, err := defaultSiteWorkspaceDir(ctx, "../demo-site"); err == nil {
		t.Fatal("expected invalid site name to fail")
	}
}

func TestEnsureLocalSiteWorkspaceBootstrapsRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	ctx := &auth.Context{
		Method:    auth.AuthMethodOAuth,
		UserEmail: "dev@example.com",
	}
	dir := filepath.Join(t.TempDir(), "demo-site")

	bootstrapped, err := ensureLocalSiteWorkspace(ctx, "demo-site", dir)
	if err != nil {
		t.Fatalf("ensureLocalSiteWorkspace returned error: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected workspace to be bootstrapped on first run")
	}

	for _, name := range []string{".git", ".gitignore", "README.md", "index.html"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}

	readmeBytes, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}
	if got := string(readmeBytes); !strings.Contains(got, "kindship site push demo-site") {
		t.Fatalf("README.md missing deploy command, got: %s", got)
	}
}

func TestEnsureLocalSiteWorkspacePreservesExistingFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	ctx := &auth.Context{
		Method:    auth.AuthMethodOAuth,
		UserEmail: "dev@example.com",
	}
	dir := filepath.Join(t.TempDir(), "demo-site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create workspace dir: %v", err)
	}

	indexPath := filepath.Join(dir, "index.html")
	const customIndex = "<html>custom</html>"
	if err := os.WriteFile(indexPath, []byte(customIndex), 0o644); err != nil {
		t.Fatalf("failed to seed index.html: %v", err)
	}

	if _, err := ensureLocalSiteWorkspace(ctx, "demo-site", dir); err != nil {
		t.Fatalf("ensureLocalSiteWorkspace returned error: %v", err)
	}

	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	if got := string(indexBytes); got != customIndex {
		t.Fatalf("expected existing index.html to be preserved, got %q", got)
	}
}

func TestPostSitePushFinalizePropagatesAttemptFields(t *testing.T) {
	var got map[string]any
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/site/push/finalize" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("expected json content type, got %q", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"attempt_id":"attempt-123","status":"committed","error_code":"NONE","commit_sha":"abcdef123","files_pushed":2}`))
	}))
	defer server.Close()

	ctx := &auth.Context{Method: auth.AuthMethodServiceKey, Token: "sk", APIBaseURL: server.URL}
	resp, err := postSitePushFinalize(ctx, api.SitePushFinalizeRequest{
		AttemptID:   "attempt-123",
		SiteName:    "demo-site",
		AgentID:     "agent-1",
		StagingPath: "demo-site/archive.tar.gz",
	})
	if err != nil {
		t.Fatalf("postSitePushFinalize returned error: %v", err)
	}
	if got["attempt_id"] != "attempt-123" {
		t.Fatalf("expected attempt_id in request, got %#v", got["attempt_id"])
	}
	if resp.AttemptID != "attempt-123" || resp.Status != "committed" || resp.ErrorCode != "NONE" {
		t.Fatalf("attempt fields not parsed: %#v", resp)
	}
}

func TestPostSitePushDryRunUsesSignedUploadFlow(t *testing.T) {
	var initBody map[string]any
	var finalizeBody map[string]any
	var uploaded []byte

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/site/push/init":
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
				t.Fatalf("dry-run init used multipart content type: %s", r.Header.Get("Content-Type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&initBody); err != nil {
				t.Fatalf("failed to decode init request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"attempt_id":"attempt-dry","status":"upload_pending","staging_path":"demo-site/dry.tar.gz","upload_url":"` + server.URL + `/signed-upload"}`))
		case "/signed-upload":
			if r.Method != http.MethodPut {
				t.Fatalf("expected PUT upload, got %s", r.Method)
			}
			uploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case "/api/cli/site/push/dry-run":
			if err := json.NewDecoder(r.Body).Decode(&finalizeBody); err != nil {
				t.Fatalf("failed to decode finalize request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"attempt_id":"attempt-dry","status":"dry_run_complete","files_in_archive":1,"changes":[{"path":"index.html","status":"modified"}],"affected_routes":["/"],"partial":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx := &auth.Context{Method: auth.AuthMethodServiceKey, Token: "sk", APIBaseURL: server.URL}
	resp, err := postSitePushDryRun(ctx, sitePushUploadArgs{
		siteName:     "demo-site",
		agentID:      "agent-1",
		message:      "check",
		partial:      true,
		archiveBytes: []byte("archive"),
		fileCount:    1,
	})
	if err != nil {
		t.Fatalf("postSitePushDryRun returned error: %v", err)
	}
	if initBody["dry_run"] != true || initBody["archive_size"].(float64) != 7 || initBody["file_count"].(float64) != 1 {
		t.Fatalf("unexpected init body: %#v", initBody)
	}
	if !bytes.Equal(uploaded, []byte("archive")) {
		t.Fatalf("unexpected upload body: %q", uploaded)
	}
	if finalizeBody["attempt_id"] != "attempt-dry" || finalizeBody["dry_run"] != true || finalizeBody["partial"] != true {
		t.Fatalf("unexpected finalize body: %#v", finalizeBody)
	}
	if resp.AttemptID != "attempt-dry" || resp.Status != "dry_run_complete" || len(resp.Changes) != 1 {
		t.Fatalf("unexpected dry-run response: %#v", resp)
	}
}

func TestPostSitePushInitReportsNonJSON413(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte("<html>too large</html>"))
	}))
	defer server.Close()

	ctx := &auth.Context{Method: auth.AuthMethodServiceKey, Token: "sk", APIBaseURL: server.URL}
	_, err := postSitePushInit(ctx, api.SitePushInitRequest{
		SiteName:    "demo-site",
		ArchiveSize: 100,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "HTTP 413") || !strings.Contains(got, "payload too large") {
		t.Fatalf("unexpected 413 error: %s", got)
	}
}

func TestFetchAndRenderSitePushStatus(t *testing.T) {
	var sawID string
	var sawAgentID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/site/push/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sawID = r.URL.Query().Get("id")
		sawAgentID = r.URL.Query().Get("agent_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"attempt":{"attempt_id":"attempt-xyz","site_name":"demo-site","status":"failed","error_code":"ARCHIVE_TOO_LARGE","error":"archive exceeded limit","archive_size":1024,"files_archived":3,"dry_run":true}}`))
	}))
	defer server.Close()

	ctx := &auth.Context{Method: auth.AuthMethodOAuth, Token: "sk", AgentID: "agent-abc", APIBaseURL: server.URL}
	resp, err := fetchSitePushStatus(ctx, "attempt-xyz")
	if err != nil {
		t.Fatalf("fetchSitePushStatus returned error: %v", err)
	}
	if sawID != "attempt-xyz" {
		t.Fatalf("expected id query, got %q", sawID)
	}
	if sawAgentID != "agent-abc" {
		t.Fatalf("expected agent_id query, got %q", sawAgentID)
	}

	output := captureStdout(t, func() {
		if err := renderSitePushStatus(*resp); err != nil {
			t.Fatalf("renderSitePushStatus returned error: %v", err)
		}
	})
	for _, want := range []string{"Push attempt: attempt-xyz", "Status:  failed", "Code:    ARCHIVE_TOO_LARGE", "Archive: 3 files"} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, output)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = original
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	return string(out)
}
