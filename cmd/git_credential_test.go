package cmd

import (
	"strings"
	"testing"
)

func TestReadGitCredentialInputFrom(t *testing.T) {
	input, err := readGitCredentialInputFrom(strings.NewReader(
		"protocol=https\nhost=kindship.ai\npath=api/agent-containers/1e4d39f5-6519-43b9-81a0-fd2230204146/git-proxy/kindship-sites/thoth-landing.git\n\n",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Protocol != "https" {
		t.Fatalf("protocol = %q", input.Protocol)
	}
	if input.Host != "kindship.ai" {
		t.Fatalf("host = %q", input.Host)
	}
	if input.Path != "api/agent-containers/1e4d39f5-6519-43b9-81a0-fd2230204146/git-proxy/kindship-sites/thoth-landing.git" {
		t.Fatalf("path = %q", input.Path)
	}
}

func TestIsKindshipGitProxyCredential(t *testing.T) {
	input := gitCredentialInput{
		Protocol: "https",
		Host:     "kindship.ai",
		Path:     "api/agent-containers/agent-123/git-proxy/kindship-sites/thoth-landing.git",
	}
	if !isKindshipGitProxyCredential(input, "https://kindship.ai", "agent-123") {
		t.Fatal("expected proxy credential input to match")
	}
	input.Path = "api/agent-containers/other/git-proxy/kindship-sites/thoth-landing.git"
	if isKindshipGitProxyCredential(input, "https://kindship.ai", "agent-123") {
		t.Fatal("expected different agent proxy path to be rejected")
	}
}

func TestGitProxyPrefixRequiresHTTPS(t *testing.T) {
	prefix, err := gitProxyPrefix("https://kindship.ai", "agent-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix != "https://kindship.ai/api/agent-containers/agent-123/git-proxy/" {
		t.Fatalf("prefix = %q", prefix)
	}

	if _, err := gitProxyPrefix("http://kindship.ai", "agent-123"); err == nil {
		t.Fatal("expected non-https API URL to fail")
	}
}

func TestRunAuthGiteaRejectsNonGitSubprocess(t *testing.T) {
	err := runAuthGitea([]string{"sh", "-c", "echo unsafe"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "only supports `git`") {
		t.Fatalf("unexpected error: %v", err)
	}
}
