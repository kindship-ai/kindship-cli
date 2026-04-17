package api

// VideoPublishResponse mirrors the JSON shape the web app returns from
// POST /api/cli/videos/publish. The revision-pinned bundle URL is what
// the UI uses to embed the Remotion <Player>; everything else is there
// so the CLI can print a useful summary.
type VideoPublishResponse struct {
	VideoID          string `json:"video_id"`
	RevisionID       string `json:"revision_id"`
	CompositionID    string `json:"composition_id"`
	DurationInFrames int    `json:"duration_in_frames"`
	FPS              int    `json:"fps"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	BundleEntryPath  string `json:"bundle_entry_path"`
	BundleURL        string `json:"bundle_url"`
	Error            string `json:"error,omitempty"`
}
