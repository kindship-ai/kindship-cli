package api

import "time"

// ExecutionMode represents how a planning entity should be executed
type ExecutionMode string

const (
	ExecutionModeLLMReasoning  ExecutionMode = "LLM_REASONING"
	ExecutionModePythonSandbox ExecutionMode = "PYTHON_SANDBOX"
	ExecutionModeHybrid        ExecutionMode = "HYBRID"
	ExecutionModeBash          ExecutionMode = "BASH"
	ExecutionModePython        ExecutionMode = "PYTHON"
	ExecutionModeAskUser       ExecutionMode = "ASK_USER"
	ExecutionModeOrchestrate   ExecutionMode = "ORCHESTRATE"
	ExecutionModeChoice        ExecutionMode = "CHOICE"
	ExecutionModeCallToAction  ExecutionMode = "CALL_TO_ACTION"
	ExecutionModeAgent         ExecutionMode = "AGENT"
)

// ExecutionAttemptStatus represents the status of an execution attempt
type ExecutionAttemptStatus string

const (
	ExecutionAttemptStatusRunning   ExecutionAttemptStatus = "RUNNING"
	ExecutionAttemptStatusSuccess   ExecutionAttemptStatus = "SUCCESS"
	ExecutionAttemptStatusFailed    ExecutionAttemptStatus = "FAILED"
	ExecutionAttemptStatusAbandoned ExecutionAttemptStatus = "ABANDONED"
)

// ValidationOutcome represents the result of a validation
type ValidationOutcome string

const (
	ValidationOutcomePass           ValidationOutcome = "PASS"
	ValidationOutcomeFail           ValidationOutcome = "FAIL"
	ValidationOutcomeWarn           ValidationOutcome = "WARN"
	ValidationOutcomeCounterfactual ValidationOutcome = "COUNTERFACTUAL"
	ValidationOutcomePartial        ValidationOutcome = "PARTIAL"
)

// ValidationSeverity represents the severity of a validation result
type ValidationSeverity string

const (
	ValidationSeverityInfo     ValidationSeverity = "INFO"
	ValidationSeverityWarning  ValidationSeverity = "WARNING"
	ValidationSeverityCritical ValidationSeverity = "CRITICAL"
)

// SuccessCriteria represents the structured criteria for entity completion
type SuccessCriteria struct {
	Description        string                 `json:"description"`
	MeasurableOutcomes []string               `json:"measurable_outcomes"`
	ValidationRules    map[string]interface{} `json:"validation_rules,omitempty"`
}

// PlanningEntity represents a planning entity from the API
type PlanningEntity struct {
	ID                  string                 `json:"id"`
	Type                string                 `json:"type"`
	Title               string                 `json:"title"`
	Description         string                 `json:"description"`
	ExecutionMode       ExecutionMode          `json:"execution_mode"`
	Status              string                 `json:"status"`
	InputSchema         map[string]interface{} `json:"input_schema"`
	OutputSchema        map[string]interface{} `json:"output_schema"`
	SuccessCriteria     SuccessCriteria        `json:"success_criteria"`
	Dependencies        []string               `json:"dependencies"`
	DependenciesLabeled map[string]string      `json:"dependencies_labeled"`
	MCPServers          []string               `json:"mcp_servers"`
	SequenceOrder       int                    `json:"sequence_order"`
	ParentID            *string                `json:"parent_id"`
	Rationale           *string                `json:"rationale"`
	AccountID           string                 `json:"account_id"`
	AgentID             string                 `json:"agent_id"`
	Code                *string                `json:"code"`
	Boundaries          map[string]interface{} `json:"boundaries"`
	Version             int                    `json:"version"`
	Tags                []string               `json:"tags"`
	ActivatedAt         *time.Time             `json:"activated_at"`
	DeletedAt           *time.Time             `json:"deleted_at"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// PendingDependency represents a labeled dependency that is not yet completed
type PendingDependency struct {
	Label    string `json:"label"`
	EntityID string `json:"entity_id"`
}

// DependencyStatus represents the status of entity dependencies
type DependencyStatus struct {
	AllMet  bool                `json:"all_met"`
	Pending []PendingDependency `json:"pending"`
}

// EntityExecuteResponse represents the response from the entity execute endpoint
type EntityExecuteResponse struct {
	Entity             PlanningEntity         `json:"entity"`
	DependenciesStatus DependencyStatus       `json:"dependencies_status"`
	Inputs             map[string]interface{} `json:"inputs"`
}

// ExecutionStartRequest represents a request to start a run
type ExecutionStartRequest struct {
	EntityID            string        `json:"entity_id"`
	ExecutionMode       ExecutionMode `json:"execution_mode"`
	AgentID             string        `json:"agent_id"`
	OrchestrationMethod string        `json:"orchestration_method,omitempty"`
	SessionID           string        `json:"session_id,omitempty"`
	ParentRun           string        `json:"parent_run,omitempty"`
}

// ExecutionStartResponse represents the response from starting an execution
type ExecutionStartResponse struct {
	ExecutionID   string                 `json:"execution_id"`
	AttemptNumber int                    `json:"attempt_number"`
	Inputs        map[string]interface{} `json:"inputs"`
}

// ExecutionOutputs represents the outputs from an execution attempt
type ExecutionOutputs struct {
	Artifacts   []string               `json:"artifacts,omitempty"`
	Metrics     map[string]interface{} `json:"metrics,omitempty"`
	Stdout      string                 `json:"stdout,omitempty"`
	Stderr      string                 `json:"stderr,omitempty"`
	Structured  map[string]interface{} `json:"structured,omitempty"` // Validated structured output extracted from stdout
	NextActions []string               `json:"next_actions,omitempty"`
}

// ValidationRecord represents a validation record to be created
type ValidationRecord struct {
	ValidationType string                 `json:"validation_type"`
	Outcome        ValidationOutcome      `json:"outcome"`
	Severity       ValidationSeverity     `json:"severity"`
	Target         string                 `json:"validation_target"`
	Actual         map[string]interface{} `json:"actual"`
	FailureReason  *string                `json:"failure_reason,omitempty"`
}

// ExecutionCompleteRequest represents a request to complete an execution
type ExecutionCompleteRequest struct {
	Status            ExecutionAttemptStatus `json:"status"`
	Outputs           *ExecutionOutputs      `json:"outputs,omitempty"`
	FailureReason     *string                `json:"failure_reason,omitempty"`
	ValidationRecords []ValidationRecord     `json:"validation_records,omitempty"`
}

// ExecutionCompleteResponse represents the response from completing an execution
type ExecutionCompleteResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// PlanNextResponse is the response from plan/next
type PlanNextResponse struct {
	Task         *TaskInfo `json:"task"`
	Message      string    `json:"message,omitempty"`
	PendingCount int       `json:"pending_count,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// TaskInfo represents a task from the plan/next API
type TaskInfo struct {
	ID                  string                 `json:"id"`
	Title               string                 `json:"title"`
	Description         string                 `json:"description"`
	Rationale           string                 `json:"rationale,omitempty"`
	SuccessCriteria     map[string]interface{} `json:"success_criteria"`
	InputSchema         map[string]interface{} `json:"input_schema"`
	OutputSchema        map[string]interface{} `json:"output_schema"`
	ExecutionMode       string                 `json:"execution_mode"`
	Code                *string                `json:"code,omitempty"`
	Boundaries          map[string]interface{} `json:"boundaries,omitempty"`
	Dependencies        []string               `json:"dependencies"`
	DependenciesLabeled map[string]string      `json:"dependencies_labeled"`
	SequenceOrder       int                    `json:"sequence_order"`
	Status              string                 `json:"status,omitempty"`
}

// ActivateEntityResponse is the response from the entity activate endpoint
type ActivateEntityResponse struct {
	ActivatedCount int      `json:"activated_count"`
	ActivatedIDs   []string `json:"activated_ids"`
	Error          string   `json:"error,omitempty"`
}

// EntityAttachment represents a text attachment on a planning entity
type EntityAttachment struct {
	ID        string                 `json:"id"`
	EntityID  string                 `json:"entity_id"`
	Type      string                 `json:"type"`
	Name      string                 `json:"name"`
	Metadata  map[string]interface{} `json:"metadata"`
	CreatedAt string                 `json:"created_at"`
	UpdatedAt string                 `json:"updated_at"`
}

// StartProcessRunResponse is the response from the process start-run endpoint
type StartProcessRunResponse struct {
	ProcessRunID         string                 `json:"process_run_id"`
	RunNumber            int                    `json:"run_number"`
	Resumed              bool                   `json:"resumed"`
	Tasks                []TaskInfo             `json:"tasks"`
	PreviousCycleOutputs map[string]interface{} `json:"previous_cycle_outputs,omitempty"`
	Attachments          []EntityAttachment     `json:"attachments,omitempty"`
	Error                string                 `json:"error,omitempty"`
}

// DevLoopTask represents a task entity returned by create-dev-loop
type DevLoopTask struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	SequenceOrder int    `json:"sequence_order"`
}

// CreateDevLoopResponse is the response from the create-dev-loop endpoint
type CreateDevLoopResponse struct {
	ProcessID string        `json:"process_id"`
	Created   bool          `json:"created"`
	Tasks     []DevLoopTask `json:"tasks,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// ResumedRun represents a run descriptor returned by recover-runs.
type ResumedRun struct {
	RunID         string `json:"run_id"`
	EntityID      string `json:"entity_id"`
	ExecutionMode string `json:"execution_mode"`
	EntityType    string `json:"entity_type"`
}

// RecoverRunsResponse is the response from the recover-runs endpoint
type RecoverRunsResponse struct {
	ResumedRuns   []ResumedRun `json:"resumed_runs"`
	ResumableRuns []ResumedRun `json:"resumable_runs,omitempty"`
	// FailedCount is retained for backward compatibility.
	// In reconciliation-first mode this is expected to remain 0.
	FailedCount    int    `json:"failed_count"`
	SkippedAskUser int    `json:"skipped_ask_user"`
	Error          string `json:"error,omitempty"`
}

// ActiveRunResponse is the response from the active-run endpoint
type ActiveRunResponse struct {
	ActiveRun *ActiveRunData `json:"active_run"`
}

// ActiveRunData represents the DB-backed active run state
type ActiveRunData struct {
	AgentID       string                 `json:"agent_id"`
	AgentSlug     string                 `json:"agent_slug"`
	EntityID      string                 `json:"entity_id"`
	RunID         string                 `json:"run_id"`
	TaskTitle     string                 `json:"task_title"`
	ExecutionMode string                 `json:"execution_mode"`
	StartedAt     string                 `json:"started_at"`
	SessionID     string                 `json:"session_id"`
	Task          *TaskInfo              `json:"task"`
	ProcessRunID  string                 `json:"process_run_id"`
	TaskIndex     int                    `json:"task_index"`
	TaskCount     int                    `json:"task_count"`
	ProcessTasks  []TaskInfo             `json:"process_tasks"`
	Inputs        map[string]interface{} `json:"inputs"`
	CycleCount    int                    `json:"cycle_count"`
}

// TaskSpec represents a task in a plan (used by both submit and export)
type TaskSpec struct {
	Title               string                 `json:"title"`
	Description         string                 `json:"description,omitempty"`
	SequenceOrder       int                    `json:"sequence_order,omitempty"`
	ExecutionMode       string                 `json:"execution_mode,omitempty"`
	Code                string                 `json:"code,omitempty"`
	DependenciesLabeled map[string]string      `json:"dependencies_labeled,omitempty"`
	InputSchema         map[string]interface{} `json:"input_schema,omitempty"`
	OutputSchema        map[string]interface{} `json:"output_schema,omitempty"`
	SuccessCriteria     *SuccessCriteria       `json:"success_criteria,omitempty"`
	Boundaries          map[string]interface{} `json:"boundaries,omitempty"`
}

// LogLine represents a single log line for streaming execution output
type LogLine struct {
	Seq     int64  `json:"seq"`
	Stream  string `json:"stream"` // stdout, stderr, system
	Content string `json:"content"`
	Ts      string `json:"ts"`
}

// SendLogLinesRequest is the request body for the log lines endpoint
type SendLogLinesRequest struct {
	Lines []LogLine `json:"lines"`
}

// SendLogLinesResponse is the response from the log lines endpoint
type SendLogLinesResponse struct {
	Inserted int    `json:"inserted"`
	Error    string `json:"error,omitempty"`
}

// ExportEntity represents a single entity in the flat export array
type ExportEntity struct {
	ID                  string                 `json:"id"`
	Type                string                 `json:"type"`
	Title               string                 `json:"title"`
	ParentID            *string                `json:"parent_id"`
	Status              string                 `json:"status"`
	Description         string                 `json:"description,omitempty"`
	SequenceOrder       *int                   `json:"sequence_order,omitempty"`
	ExecutionMode       string                 `json:"execution_mode,omitempty"`
	Code                string                 `json:"code,omitempty"`
	RecurrencePattern   string                 `json:"recurrence_pattern,omitempty"`
	Tags                []string               `json:"tags,omitempty"`
	Boundaries          map[string]interface{} `json:"boundaries,omitempty"`
	SuccessCriteria     map[string]interface{} `json:"success_criteria,omitempty"`
	DependenciesLabeled map[string]interface{} `json:"dependencies_labeled,omitempty"`
	InputSchema         map[string]interface{} `json:"input_schema,omitempty"`
	OutputSchema        map[string]interface{} `json:"output_schema,omitempty"`
}

// ExportResponse is the response from plan/export (flat array format)
type ExportResponse struct {
	Entities []ExportEntity  `json:"entities"`
	Metadata *ExportMetadata `json:"_metadata,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// ExportMetadata contains summary info for the export
type ExportMetadata struct {
	RootID        string `json:"root_id"`
	AgentID       string `json:"agent_id"`
	TotalEntities int    `json:"total_entities"`
	Depth         int    `json:"depth"`
	ExportedAt    string `json:"exported_at"`
}

// ImportResponse is the response from plan/import
type ImportResponse struct {
	Success          bool           `json:"success"`
	CreatedCount     int            `json:"created_count"`
	UpdatedCount     int            `json:"updated_count"`
	Entities         []ImportResult `json:"entities"`
	ObjectiveID      string         `json:"objective_id"`
	PrimeDirectiveID string         `json:"prime_directive_id"`
	Error            string         `json:"error,omitempty"`
}

// ImportResult represents the outcome of a single entity import
type ImportResult struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Action    string `json:"action"`              // "created", "updated", or "skipped"
	Error     string `json:"error,omitempty"`      // reason if skipped
	ErrorCode string `json:"error_code,omitempty"` // PostgreSQL error code if skipped
}
