package api

// VoiceGenerateRequest is the JSON body sent to /api/cli/voice/generate.
// Fields are snake_case to match the Zod schema on the server.
type VoiceGenerateRequest struct {
	AgentID             string `json:"agent_id,omitempty"`
	Slug                string `json:"slug"`
	Narrative           string `json:"narrative"`
	VoiceOverride       string `json:"voice_override,omitempty"`
	StyleOverride       string `json:"style_override,omitempty"`
	PersonalityOverride string `json:"personality_override,omitempty"`
	TargetMinutes       int    `json:"target_minutes,omitempty"`
}

// VoiceExactRequest is the JSON body sent to /api/cli/voice/exact.
// No Opus pre-processing — each request renders one short clip.
type VoiceExactRequest struct {
	AgentID string `json:"agent_id,omitempty"`
	Voice   string `json:"voice"`
	Style   string `json:"style"`
	Text    string `json:"text"`
}

// VoicePodcastRequest is the JSON body sent to /api/cli/voice/podcast.
type VoicePodcastRequest struct {
	AgentID                      string `json:"agent_id,omitempty"`
	Slug                         string `json:"slug"`
	Narrative                    string `json:"narrative"`
	NarratorVoiceOverride        string `json:"narrator_voice_override,omitempty"`
	NarratorStyleOverride        string `json:"narrator_style_override,omitempty"`
	NarratorPersonalityOverride  string `json:"narrator_personality_override,omitempty"`
	NarratorNameOverride         string `json:"narrator_name_override,omitempty"`
	CompanionVoiceOverride       string `json:"companion_voice_override,omitempty"`
	CompanionStyleOverride       string `json:"companion_style_override,omitempty"`
	CompanionPersonalityOverride string `json:"companion_personality_override,omitempty"`
	CompanionNameOverride        string `json:"companion_name_override,omitempty"`
	TargetLength                 string `json:"target_length,omitempty"`
}

// VoiceErrorResponse is the shape the server returns on any non-200 outcome.
type VoiceErrorResponse struct {
	Error string `json:"error,omitempty"`
}

// VoiceExactBatchFile is the driver file shape for `kindship voice exact
// --texts-file`. Each text renders as its own WAV via one API call.
type VoiceExactBatchFile struct {
	Texts []string `json:"texts"`
}

// VoiceExactManifestEntry is one line in the out/manifest.json produced
// by the `kindship voice exact --texts-file ... --output-dir out/` flow.
type VoiceExactManifestEntry struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
	File  string `json:"file"`
	Bytes int64  `json:"bytes"`
}

// VoiceExactManifest is the full manifest written to out/manifest.json.
type VoiceExactManifest struct {
	Voice   string                    `json:"voice"`
	Style   string                    `json:"style"`
	Entries []VoiceExactManifestEntry `json:"entries"`
}
