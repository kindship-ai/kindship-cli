package api

import "time"

// ExecutionMode represents how a planning entity should be executed
type ExecutionMode string

const (
	ExecutionModeLLMReasoning  ExecutionMode = "LLM_REASONING"
	ExecutionModePythonSandbox ExecutionMode = "PYTHON_SANDBOX"
	ExecutionModeHybrid        ExecutionMode = "HYBRID"
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
	ValidationOutcomePass            ValidationOutcome = "PASS"
	ValidationOutcomeFail            ValidationOutcome = "FAIL"
	ValidationOutcomeWarn            ValidationOutcome = "WARN"
	ValidationOutcomeCounterfactual  ValidationOutcome = "COUNTERFACTUAL"
	ValidationOutcomePartial         ValidationOutcome = "PARTIAL"
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
	ValidationRules    map[string]interface{} `json:"validation_rules"`
}

// PlanningEntity represents a planning entity from the API
type PlanningEntity struct {
	ID                   string                 `json:"id"`
	Type                 string                 `json:"type"`
	Title                string                 `json:"title"`
	Description          string                 `json:"description"`
	ExecutionMode        ExecutionMode          `json:"execution_mode"`
	Status               string                 `json:"status"`
	InputSchema          map[string]interface{} `json:"input_schema"`
	OutputSchema         map[string]interface{} `json:"output_schema"`
	SuccessCriteria      SuccessCriteria        `json:"success_criteria"`
	Dependencies         []string               `json:"dependencies"`
	DependenciesLabeled  map[string]string      `json:"dependencies_labeled"`
	MCPServers           []string               `json:"mcp_servers"`
	SequenceOrder        int                    `json:"sequence_order"`
	ParentID             *string                `json:"parent_id"`
	Rationale            *string                `json:"rationale"`
	AccountID            string                 `json:"account_id"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
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
	EntityID      string        `json:"entity_id"`
	ExecutionMode ExecutionMode `json:"execution_mode"`
	AgentID       string        `json:"agent_id"`
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
