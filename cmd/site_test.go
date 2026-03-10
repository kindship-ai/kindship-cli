package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
