package api

// EntityGetResponse wraps a single entity with optional includes
type EntityGetResponse struct {
	Entity       PlanningEntity   `json:"entity"`
	Children     []PlanningEntity `json:"children,omitempty"`
	Runs         []EntityRun      `json:"runs,omitempty"`
	Dependencies []EntityDep      `json:"dependencies,omitempty"`
	Parent       *PlanningEntity  `json:"parent,omitempty"`
}

// EntityRun is a summary of an execution run on an entity
type EntityRun struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	AttemptNumber int     `json:"attempt_number"`
	StartedAt     string  `json:"started_at"`
	CompletedAt   *string `json:"completed_at,omitempty"`
}

// EntityDep represents a dependency relationship
type EntityDep struct {
	EntityID     string `json:"entity_id"`
	DependsOnID  string `json:"depends_on_id"`
	DependsOnTitle string `json:"depends_on_title,omitempty"`
	Status       string `json:"status,omitempty"`
}

// EntityListResponse wraps list results
type EntityListResponse struct {
	Entities []PlanningEntity `json:"entities"`
	Count    int              `json:"count"`
}

// EntityCreateRequest contains fields for creating an entity
type EntityCreateRequest struct {
	Type            string                 `json:"type"`
	ParentID        *string                `json:"parent_id,omitempty"`
	Title           string                 `json:"title"`
	Description     string                 `json:"description,omitempty"`
	Status          string                 `json:"status,omitempty"`
	ExecutionMode   string                 `json:"execution_mode,omitempty"`
	Code            *string                `json:"code,omitempty"`
	SequenceOrder   *int                   `json:"sequence_order,omitempty"`
	Tags            []string               `json:"tags,omitempty"`
	InputSchema     map[string]interface{} `json:"input_schema,omitempty"`
	OutputSchema    map[string]interface{} `json:"output_schema,omitempty"`
	SuccessCriteria *SuccessCriteria       `json:"success_criteria,omitempty"`
	Boundaries      map[string]interface{} `json:"boundaries,omitempty"`
}

// EntityCreateResponse is the response from entity creation
type EntityCreateResponse struct {
	Entity PlanningEntity `json:"entity"`
}

// EntityUpdateRequest contains fields for updating an entity
type EntityUpdateRequest struct {
	Title           *string                `json:"title,omitempty"`
	Description     *string                `json:"description,omitempty"`
	Status          *string                `json:"status,omitempty"`
	ExecutionMode   *string                `json:"execution_mode,omitempty"`
	Code            *string                `json:"code,omitempty"`
	Tags            []string               `json:"tags,omitempty"`
	ExpectedVersion *int                   `json:"expected_version,omitempty"`
	InputSchema     map[string]interface{} `json:"input_schema,omitempty"`
	OutputSchema    map[string]interface{} `json:"output_schema,omitempty"`
	SuccessCriteria *SuccessCriteria       `json:"success_criteria,omitempty"`
	Boundaries      map[string]interface{} `json:"boundaries,omitempty"`
}

// EntityUpdateResponse is the response from entity update
type EntityUpdateResponse struct {
	Entity PlanningEntity `json:"entity"`
}

// EntityDeleteResponse is the response from entity deletion
type EntityDeleteResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// EntityRestoreResponse is the response from entity restore
type EntityRestoreResponse struct {
	Entity PlanningEntity `json:"entity"`
}

// EntityDepsRequest is the body for managing dependencies
type EntityDepsRequest struct {
	DependencyID string `json:"dependency_id"`
}

// EntityDepsResponse is the response from dependency operations
type EntityDepsResponse struct {
	Success      bool        `json:"success"`
	Message      string      `json:"message,omitempty"`
	Dependencies []EntityDep `json:"dependencies,omitempty"`
}

// EntityMoveRequest is the body for moving an entity
type EntityMoveRequest struct {
	ParentID      string `json:"parent_id"`
	SequenceOrder *int   `json:"sequence_order,omitempty"`
}

// EntityMoveResponse is the response from entity move
type EntityMoveResponse struct {
	Entity PlanningEntity `json:"entity"`
}

// ListEntitiesOpts contains options for listing entities
type ListEntitiesOpts struct {
	AgentID        string
	Type           string
	Status         string
	ParentID       string
	Limit          int
	IncludeDeleted bool
}
