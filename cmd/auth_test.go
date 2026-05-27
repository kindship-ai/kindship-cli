package cmd

import (
	"reflect"
	"testing"
)

func TestParseAuthExecUsesSecretCommandAsExecutableByDefault(t *testing.T) {
	execCommand, execArgs, err := parseAuthExec("claude", []string{"-p", "hi"})
	if err != nil {
		t.Fatalf("parseAuthExec returned error: %v", err)
	}
	if execCommand != "claude" {
		t.Fatalf("exec command = %q, want claude", execCommand)
	}
	if !reflect.DeepEqual(execArgs, []string{"-p", "hi"}) {
		t.Fatalf("exec args = %#v", execArgs)
	}
}

func TestParseAuthExecSupportsDelimiterForVaultWrapper(t *testing.T) {
	execCommand, execArgs, err := parseAuthExec(
		"vault",
		[]string{"--", "sh", "-lc", "test -n \"$TEST_KEY\""},
	)
	if err != nil {
		t.Fatalf("parseAuthExec returned error: %v", err)
	}
	if execCommand != "sh" {
		t.Fatalf("exec command = %q, want sh", execCommand)
	}
	if !reflect.DeepEqual(execArgs, []string{"-lc", "test -n \"$TEST_KEY\""}) {
		t.Fatalf("exec args = %#v", execArgs)
	}
}

func TestParseAuthExecRejectsMissingExecutableAfterDelimiter(t *testing.T) {
	_, _, err := parseAuthExec("vault", []string{"--"})
	if err == nil {
		t.Fatal("expected error")
	}
}
