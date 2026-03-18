package executor

import (
	"encoding/json"
	"strings"
)

const completionStdoutFallbackLimit = 64 << 10 // 64 KB

// reduceStreamJSONForCompletion keeps only the terminal stream-json result
// object for completion payloads. Full event history is already persisted via
// run_log_lines, so returning only the final result dramatically reduces the
// size of /execution/complete requests.
func reduceStreamJSONForCompletion(stdout string) string {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return stdout
	}

	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}

		if envelope.Type == "result" {
			return line
		}
	}

	if len(stdout) <= completionStdoutFallbackLimit {
		return stdout
	}

	return stdout[:completionStdoutFallbackLimit]
}
