package cmd

import (
	"strings"
	"testing"
)

func TestReadGitCredentialInputFrom(t *testing.T) {
	input, err := readGitCredentialInputFrom(strings.NewReader(
		"protocol=https\nhost=gitea.kindship.ai\npath=kindship-sites/thoth-landing.git\n\n",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Protocol != "https" {
		t.Fatalf("protocol = %q", input.Protocol)
	}
	if input.Host != "gitea.kindship.ai" {
		t.Fatalf("host = %q", input.Host)
	}
	if input.Path != "kindship-sites/thoth-landing.git" {
		t.Fatalf("path = %q", input.Path)
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
