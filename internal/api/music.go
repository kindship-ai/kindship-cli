package api

// MusicGenerateRequest is the JSON body sent to /api/cli/music/generate.
// Fields are lowercase_snake to match the Zod schema on the server.
type MusicGenerateRequest struct {
	AgentID      string `json:"agent_id,omitempty"`
	Slug         string `json:"slug"`
	Prompt       string `json:"prompt"`
	DurationMs   int    `json:"duration_ms"`
	OutputFormat string `json:"output_format,omitempty"`
}

// MusicGenerateErrorResponse is the shape the server returns on any
// non-200 outcome. The Axiom-friendly field is `error`; we don't decode
// anything else because everything non-200 maps to a CLI error.
type MusicGenerateErrorResponse struct {
	Error string `json:"error,omitempty"`
}
