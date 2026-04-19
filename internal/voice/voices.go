// Package voice holds the voice-pipeline primitives: Gemini voice
// roster, STYLE.md Sound-section parser, and the Gemini prompt
// builders used by `kindship voice {,exact,multi}`.
//
// The roster mirrors apps/web (pre-restructure) so container-side
// parsing matches server-side validation. Adding a voice here + in
// the web repo is the expected change when Google ships a new voice.
package voice

// Gemini prebuilt voice roster — the 30 voices supported by both
// gemini-3.1-flash-live-preview (Live API, one voice per session) and
// gemini-3.1-flash-tts-preview (TTS, single or multi-speaker). Voice
// identity is preserved across the two APIs, so picking "Kore" in
// STYLE.md gives the same speaker no matter which renderer runs.
//
// Source: https://ai.google.dev/gemini-api/docs/speech-generation#voices
var GeminiVoiceNames = [...]string{
	"Zephyr", "Puck", "Charon", "Kore", "Fenrir",
	"Leda", "Orus", "Aoede", "Callirrhoe", "Autonoe",
	"Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina",
	"Erinome", "Algenib", "Rasalgethi", "Laomedeia", "Achernar",
	"Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird",
	"Zubenelgenubi", "Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat",
}

// IsValidGeminiVoice reports whether name is one of the 30 prebuilt
// voices. Parsing routines use this to reject garbage inside
// STYLE.md `[voice, clause]` brackets.
func IsValidGeminiVoice(name string) bool {
	for _, v := range GeminiVoiceNames {
		if v == name {
			return true
		}
	}
	return false
}
