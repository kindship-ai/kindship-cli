package api

// SiteUploadFile is one entry in a SiteInitRequest — the path the file
// will land at under the revision's S3 key prefix, plus its byte size
// and content type so the server can presign a tight PUT URL.
type SiteUploadFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
}

// SiteInitRequest is the body for POST /api/cli/videos/site/init.
// AgentID is optional when the caller authenticates with a service key
// that already binds to one agent; bearer auth populates it from the
// CLI's active agent context.
type SiteInitRequest struct {
	AgentID    string           `json:"agent_id,omitempty"`
	VideoID    string           `json:"video_id"`
	RevisionID string           `json:"revision_id"`
	Files      []SiteUploadFile `json:"files"`
}

// SitePresignedUpload is one presigned PUT slot returned by
// /api/cli/videos/site/init. The CLI PUTs the file body to URL with
// exactly the headers the server signed; deviating breaks the signature.
type SitePresignedUpload struct {
	Path    string            `json:"path"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// SiteInitResponse is the response from POST /api/cli/videos/site/init.
// Bucket + KeyPrefix are informational — the CLI doesn't need them for
// the upload itself, but they're handy for log lines and debugging.
type SiteInitResponse struct {
	Bucket    string                `json:"bucket"`
	KeyPrefix string                `json:"key_prefix"`
	Uploads   []SitePresignedUpload `json:"uploads"`
	Error     string                `json:"error,omitempty"`
}

// SiteFinalizeRequest is the body for POST /api/cli/videos/site/finalize.
// ExpectedFiles must match the paths the CLI just PUT to S3; the server
// runs HEAD against each one to confirm the upload landed before flipping
// lambda_site_bucket on the revision row.
type SiteFinalizeRequest struct {
	AgentID       string   `json:"agent_id,omitempty"`
	VideoID       string   `json:"video_id"`
	RevisionID    string   `json:"revision_id"`
	ExpectedFiles []string `json:"expected_files"`
}

// SiteFinalizeResponse is the response from POST /api/cli/videos/site/finalize.
// Missing is populated when the server's HEAD probe found gaps — CLI
// re-uploads just those paths and re-finalizes.
type SiteFinalizeResponse struct {
	Ready      bool     `json:"ready"`
	RevisionID string   `json:"revision_id"`
	Missing    []string `json:"missing,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// VideoListEntry is one row in VideoListResponse — flat shape suited to
// tabular CLI output. HasMP4Render mirrors the UI's Download-button gate
// (true when lambda_site_bucket is set on the current revision).
type VideoListEntry struct {
	Slug              string `json:"slug"`
	Title             string `json:"title"`
	CurrentRevisionID string `json:"current_revision_id"`
	UpdatedAt         string `json:"updated_at"`
	HasMP4Render      bool   `json:"has_mp4_render"`
	MP4Status         string `json:"mp4_status"`
}

// VideoListResponse is the response from GET /api/cli/videos/list.
type VideoListResponse struct {
	Videos []VideoListEntry `json:"videos"`
	Error  string           `json:"error,omitempty"`
}

// VideoStatusVideo is the video-row half of VideoStatusResponse.
type VideoStatusVideo struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
}

// VideoStatusRevision is the revision-row half of VideoStatusResponse.
// HasSiteDeployed is the server-side projection of `lambda_site_bucket
// IS NOT NULL` — the CLI uses it to decide whether `render` needs to
// upload a fresh webpack site bundle or can skip straight to Lambda.
type VideoStatusRevision struct {
	ID                   string  `json:"id"`
	CompositionID        string  `json:"composition_id"`
	Width                int     `json:"width"`
	Height               int     `json:"height"`
	FPS                  int     `json:"fps"`
	DurationInFrames     int     `json:"duration_in_frames"`
	PublishedAt          string  `json:"published_at"`
	BundleStoragePrefix  string  `json:"bundle_storage_prefix"`
	BundleEntryPath      string  `json:"bundle_entry_path"`
	LambdaSiteBucket     *string `json:"lambda_site_bucket"`
	LambdaSiteKeyPrefix  *string `json:"lambda_site_key_prefix"`
	HasSiteDeployed      bool    `json:"has_site_deployed"`
	MP4RenderStatus      string  `json:"mp4_render_status"`
	MP4RenderCompletedAt *string `json:"mp4_render_completed_at"`
	MP4RenderOutputKey   *string `json:"mp4_render_output_key"`
	MP4RenderError       *string `json:"mp4_render_error"`
}

// VideoStatusResponse is the response from GET /api/cli/videos/[slug]/status.
// Revision is null when the video row exists but `current_revision_id`
// is NULL (publish failed mid-way); CLI surfaces this as "no revision
// yet — try `kindship video publish` again".
type VideoStatusResponse struct {
	Video    *VideoStatusVideo    `json:"video,omitempty"`
	Revision *VideoStatusRevision `json:"revision"`
	Error    string               `json:"error,omitempty"`
}
