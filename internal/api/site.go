package api

// SiteInfo represents a site returned from the API
type SiteInfo struct {
	ID               string  `json:"id"`
	SiteName         string  `json:"site_name"`
	Domain           string  `json:"domain"`
	Status           string  `json:"status"`
	GiteaRepoURL     *string `json:"gitea_repo_url"`
	WoodpeckerRepoID *int    `json:"woodpecker_repo_id"`
	LastDeployAt     *string `json:"last_deploy_at"`
	LastDeploySha    *string `json:"last_deploy_sha"`
	LastError        *string `json:"last_error"`
	CustomDomain     *string `json:"custom_domain"`
	CreatedAt        string  `json:"created_at"`
}

// SiteBuild represents build info from the status endpoint
type SiteBuildError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	IsWarning bool   `json:"is_warning"`
}

type SiteBuild struct {
	Number     int              `json:"number"`
	Status     string           `json:"status"`
	Commit     string           `json:"commit"`
	StartedAt  int64            `json:"started_at"`
	FinishedAt int64            `json:"finished_at"`
	Errors     []SiteBuildError `json:"errors,omitempty"`
}

// ServedDeploy describes what the web host is currently known to serve.
type ServedDeploy struct {
	CommitSha       *string `json:"commit_sha"`
	DeployedAt      *string `json:"deployed_at"`
	Status          string  `json:"status"`
	ContainerStatus *string `json:"container_status"`
	Error           *string `json:"error"`
}

// SiteBuildStep represents a single step in build logs
type SiteBuildStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output"`
}

// SiteCreateResponse is the response from POST /api/cli/site/create
type SiteCreateResponse struct {
	Site                  *SiteInfo `json:"site,omitempty"`
	WorkspaceDir          string    `json:"workspace_dir,omitempty"`
	WorkspaceBootstrapped bool      `json:"workspace_bootstrapped"`
	Error                 string    `json:"error,omitempty"`
}

// SiteListResponse is the response from GET /api/cli/site/list
type SiteListResponse struct {
	Sites []SiteInfo `json:"sites"`
	Error string     `json:"error,omitempty"`
}

// SiteStatusResponse is the response from GET /api/cli/site/status
type SiteStatusResponse struct {
	Site              *SiteInfo        `json:"site,omitempty"`
	Build             *SiteBuild       `json:"build"`
	ServedDeploy      *ServedDeploy    `json:"served_deploy,omitempty"`
	LatestPushAttempt *SitePushAttempt `json:"latest_push_attempt,omitempty"`
	Error             string           `json:"error,omitempty"`
}

// SitePushResponse is the response from POST /api/cli/site/push (legacy
// multipart endpoint) and from /api/cli/site/push/finalize (the second
// step of the new two-step flow). Both share the same success shape so
// callers can render either path identically.
type SitePushResponse struct {
	AttemptID       string   `json:"attempt_id,omitempty"`
	Status          string   `json:"status,omitempty"`
	ErrorCode       string   `json:"error_code,omitempty"`
	CommitSha       string   `json:"commit_sha"`
	BuildNumber     int      `json:"build_number,omitempty"`
	BuildStatus     string   `json:"build_status,omitempty"`
	BuildCommit     string   `json:"build_commit,omitempty"`
	BuildStartedAt  int64    `json:"build_started_at,omitempty"`
	BuildFinishedAt int64    `json:"build_finished_at,omitempty"`
	FilesPushed     int      `json:"files_pushed"`
	Message         string   `json:"message"`
	SourceDir       string   `json:"source_dir,omitempty"`
	SkippedReserved []string `json:"skipped_reserved"`
	SkippedDenied   []string `json:"skipped_denied"`
	Error           string   `json:"error,omitempty"`
}

// SitePushInitRequest is the request body for POST /api/cli/site/push/init.
// archive_size lets the server reject pushes above the staging bucket cap
// before the CLI wastes an upload.
type SitePushInitRequest struct {
	SiteName    string `json:"site_name"`
	AgentID     string `json:"agent_id,omitempty"`
	ArchiveSize int64  `json:"archive_size"`
	FileCount   int    `json:"file_count,omitempty"`
	Partial     bool   `json:"partial,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
}

// SitePushInitResponse is the response from POST /api/cli/site/push/init.
// upload_url is a short-lived Supabase Storage signed PUT URL; the CLI
// uploads the tar.gz directly to it (no Vercel function body cap).
type SitePushInitResponse struct {
	AttemptID   string `json:"attempt_id,omitempty"`
	Status      string `json:"status,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	StagingPath string `json:"staging_path"`
	UploadURL   string `json:"upload_url"`
	UploadToken string `json:"upload_token"`
	Error       string `json:"error,omitempty"`
}

// SitePushFinalizeRequest is the request body for POST /api/cli/site/push/finalize.
// staging_path must match the path returned from /init for the same site.
type SitePushFinalizeRequest struct {
	AttemptID   string `json:"attempt_id,omitempty"`
	SiteName    string `json:"site_name"`
	AgentID     string `json:"agent_id,omitempty"`
	StagingPath string `json:"staging_path"`
	Message     string `json:"message,omitempty"`
	Partial     bool   `json:"partial,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
}

// SitePushDryRunChange describes a single file that would change in a push.
// Status values mirror git diff semantics: "added", "modified", "deleted".
type SitePushDryRunChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Size   int    `json:"size,omitempty"`
}

// SitePushDryRunResponse is the response from POST /api/cli/site/push/dry-run.
// Reports what a push would change without triggering a build.
type SitePushDryRunResponse struct {
	AttemptID       string                 `json:"attempt_id,omitempty"`
	Status          string                 `json:"status,omitempty"`
	ErrorCode       string                 `json:"error_code,omitempty"`
	FilesInArchive  int                    `json:"files_in_archive"`
	Changes         []SitePushDryRunChange `json:"changes"`
	AffectedRoutes  []string               `json:"affected_routes"`
	SkippedReserved []string               `json:"skipped_reserved"`
	SkippedDenied   []string               `json:"skipped_denied"`
	Partial         bool                   `json:"partial"`
	Error           string                 `json:"error,omitempty"`
}

// SitePushAttempt is the durable server-side attempt record surfaced by
// push-status and, when available, embedded into site status.
type SitePushAttempt struct {
	AttemptID           string  `json:"attempt_id"`
	SiteName            string  `json:"site_name,omitempty"`
	Status              string  `json:"status"`
	ErrorCode           string  `json:"error_code,omitempty"`
	Error               string  `json:"error,omitempty"`
	CommitSha           string  `json:"commit_sha,omitempty"`
	FilesPushed         int     `json:"files_pushed,omitempty"`
	FilesArchived       int     `json:"files_archived,omitempty"`
	ArchiveSize         int64   `json:"archive_size,omitempty"`
	BaselineBuildNumber int     `json:"baseline_build_number,omitempty"`
	BuildNumber         int     `json:"build_number,omitempty"`
	BuildStatus         string  `json:"build_status,omitempty"`
	BuildCommit         string  `json:"build_commit,omitempty"`
	BuildStartedAt      int64   `json:"build_started_at,omitempty"`
	BuildFinishedAt     int64   `json:"build_finished_at,omitempty"`
	ResolvedFromBuild   bool    `json:"resolved_from_build,omitempty"`
	Partial             bool    `json:"partial,omitempty"`
	DryRun              bool    `json:"dry_run,omitempty"`
	CreatedAt           string  `json:"created_at,omitempty"`
	UpdatedAt           string  `json:"updated_at,omitempty"`
	CompletedAt         *string `json:"completed_at,omitempty"`
}

// SitePushStatusResponse is the response from GET /api/cli/site/push/status.
type SitePushStatusResponse struct {
	Attempt *SitePushAttempt `json:"attempt,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// SiteLogsResponse is the response from GET /api/cli/site/logs
type SiteLogsResponse struct {
	BuildNumber int             `json:"build_number"`
	Status      string          `json:"status"`
	Steps       []SiteBuildStep `json:"steps"`
	Error       string          `json:"error,omitempty"`
}

// SiteDeleteResponse is the response from POST /api/cli/site/delete
type SiteDeleteResponse struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SiteDomainResponse is the response from custom domain set/status endpoints
type SiteDomainResponse struct {
	CustomDomain      string `json:"custom_domain"`
	Status            string `json:"status"`
	SSLStatus         string `json:"ssl_status"`
	CnameTarget       string `json:"cname_target"`
	DnsAutoConfigured bool   `json:"dns_auto_configured"`
	DnsProvider       string `json:"dns_provider,omitempty"`
	DnsError          string `json:"dns_error,omitempty"`
	Error             string `json:"error,omitempty"`
}

// SiteDomainRemoveResponse is the response from DELETE /api/cli/site/domain
type SiteDomainRemoveResponse struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SiteVerifyResponse is the response from GET /api/cli/site/verify.
//
// All "absent or unparseable" cases collapse to nil — the receipt
// reports what was observed, including 404s and missing sitemaps,
// without treating them as errors. The CLI consumer applies its own
// pass/fail threshold against these raw fields.
//
// EdgeStatus is non-pointer because the edge probe is always attempted
// and reports 0 when the probe itself failed (network error). Every
// other field is a pointer so absence is distinguishable from zero.
type SiteVerifyResponse struct {
	SiteName              string  `json:"site_name"`
	CanonicalURL          string  `json:"canonical_url"`
	FinalURL              *string `json:"final_url"`
	EdgeStatus            int     `json:"edge_status"`
	SitemapURL            *string `json:"sitemap_url"`
	SitemapStatus         *int    `json:"sitemap_status"`
	SitemapIsXML          *bool   `json:"sitemap_is_xml"`
	SitemapURLCount       *int    `json:"sitemap_url_count"`
	RoutePresentInSitemap *bool   `json:"route_present_in_sitemap"`
	RouteStatus           *int    `json:"route_status"`
	Error                 string  `json:"error,omitempty"`
}

// SiteAnalyticsContext is the shared context returned by analytics endpoints.
type SiteAnalyticsContext struct {
	Site  SiteAnalyticsSite  `json:"site"`
	Range SiteAnalyticsRange `json:"range"`
}

// SiteAnalyticsSite identifies the Kindship site and its provisioned Umami
// website without exposing Umami credentials.
type SiteAnalyticsSite struct {
	ID                 string  `json:"id"`
	SiteName           string  `json:"site_name"`
	Domain             string  `json:"domain"`
	CustomDomain       *string `json:"custom_domain"`
	AnalyticsWebsiteID string  `json:"analytics_website_id"`
	AnalyticsScriptURL *string `json:"analytics_script_url,omitempty"`
}

// SiteAnalyticsRange describes the exact time window queried.
type SiteAnalyticsRange struct {
	Range   string `json:"range"`
	StartAt int64  `json:"start_at"`
	EndAt   int64  `json:"end_at"`
	Unit    string `json:"unit"`
}

// SiteAnalyticsSummaryResponse is the response from
// GET /api/cli/site/analytics/summary.
type SiteAnalyticsSummaryResponse struct {
	SiteAnalyticsContext
	Summary map[string]interface{} `json:"summary"`
	Error   string                 `json:"error,omitempty"`
}

// SiteAnalyticsPageviewsResponse is the response from
// GET /api/cli/site/analytics/pageviews.
type SiteAnalyticsPageviewsResponse struct {
	SiteAnalyticsContext
	Pageviews interface{} `json:"pageviews"`
	Error     string      `json:"error,omitempty"`
}

// SiteAnalyticsMetricsResponse is the response from
// GET /api/cli/site/analytics/metrics.
type SiteAnalyticsMetricsResponse struct {
	SiteAnalyticsContext
	Metric  string        `json:"metric"`
	Limit   int           `json:"limit"`
	Metrics []interface{} `json:"metrics"`
	Error   string        `json:"error,omitempty"`
}

// SiteAnalyticsEventsResponse is the response from
// GET /api/cli/site/analytics/events.
type SiteAnalyticsEventsResponse struct {
	SiteAnalyticsContext
	Limit      int                    `json:"limit"`
	EventStats map[string]interface{} `json:"event_stats"`
	Events     []interface{}          `json:"events"`
	Error      string                 `json:"error,omitempty"`
}

// SiteAnalyticsEventDataResponse is the response from
// GET /api/cli/site/analytics/event-data.
type SiteAnalyticsEventDataResponse struct {
	SiteAnalyticsContext
	Event     *string       `json:"event"`
	Property  *string       `json:"property"`
	EventData []interface{} `json:"event_data"`
	Error     string        `json:"error,omitempty"`
}

// SiteAnalyticsHealthResponse is the response from
// GET /api/cli/site/analytics/health.
type SiteAnalyticsHealthResponse struct {
	SiteAnalyticsContext
	Health map[string]interface{} `json:"health"`
	Error  string                 `json:"error,omitempty"`
}
