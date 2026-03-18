package executor

import (
	"strings"
	"testing"
)

func TestReduceStreamJSONForCompletionReturnsLastResultObject(t *testing.T) {
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}
{"type":"result","subtype":"success","result":"first"}
{"type":"result","subtype":"success","result":"final"}`

	got := reduceStreamJSONForCompletion(input)
	want := `{"type":"result","subtype":"success","result":"final"}`

	if got != want {
		t.Fatalf("expected final result object %q, got %q", want, got)
	}
}

func TestReduceStreamJSONForCompletionFallsBackWhenNoResultObject(t *testing.T) {
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`

	got := reduceStreamJSONForCompletion(input)
	if got != input {
		t.Fatalf("expected original stdout when no result object is present")
	}
}

func TestReduceStreamJSONForCompletionTruncatesFallback(t *testing.T) {
	input := strings.Repeat("a", completionStdoutFallbackLimit+10)

	got := reduceStreamJSONForCompletion(input)
	if len(got) != completionStdoutFallbackLimit {
		t.Fatalf("expected truncated stdout length %d, got %d", completionStdoutFallbackLimit, len(got))
	}
}
