package api

// User Messaging — first-class agent → user channel.
//
// See /Users/Testsson/.claude/plans/snazzy-leaping-stonebraker.md.
// Backend lives in the kindship-vercel repo under apps/web/app/api/cli/user-messaging/.

type UserMessageChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// UserMessage mirrors the public.user_messages row.
type UserMessage struct {
	ID              string              `json:"id"`
	AccountID       string              `json:"account_id"`
	AgentID         string              `json:"agent_id"`
	Type            string              `json:"type"`
	Agency          string              `json:"agency"`
	Title           *string             `json:"title"`
	Body            string              `json:"body"`
	Choices         []UserMessageChoice `json:"choices"`
	Status          string              `json:"status"`
	CreatedAt       string              `json:"created_at"`
	UpdatedAt       string              `json:"updated_at"`
	AnsweredAt      *string             `json:"answered_at"`
	RetractedAt     *string             `json:"retracted_at"`
	ExpiresAt       *string             `json:"expires_at"`
	AnswerText      *string             `json:"answer_text"`
	AnswerChoice    *string             `json:"answer_choice"`
	AnswerApproval  *bool               `json:"answer_approval"`
	AnsweredVia     *string             `json:"answered_via"`
	LastRemindedAt  *string             `json:"last_reminded_at"`
	NextReminderAt  *string             `json:"next_reminder_at"`
	ReminderCount   int                 `json:"reminder_count"`
	RunID           *string             `json:"run_id"`
	ParentEntityID  *string             `json:"parent_entity_id"`
	IsTestFixture   bool                `json:"is_test_fixture"`
	DisposedAt      *string             `json:"disposed_at"`
	DispositionCode *string             `json:"disposition_code"`
	DispositionNote *string             `json:"disposition_note"`
}

type UserMessagingDisposeRequest struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Note string `json:"note,omitempty"`
}

type UserMessagingDisposeResponse struct {
	Ok         bool         `json:"ok"`
	Row        *UserMessage `json:"row,omitempty"`
	Status     string       `json:"status,omitempty"`
	DisposedAt *string      `json:"disposedAt,omitempty"`
	Error      string       `json:"error,omitempty"`
}

type UserMessagingSendRequest struct {
	AgentID          string              `json:"agent_id"`
	Type             string              `json:"type"` // question | choice | approval | report
	Agency           string              `json:"agency"`
	Body             string              `json:"body"`
	Title            string              `json:"title,omitempty"`
	Choices          []UserMessageChoice `json:"choices,omitempty"`
	ExpiresInSeconds int                 `json:"expires_in_seconds,omitempty"`
	RunID            string              `json:"run_id,omitempty"`
	ParentEntityID   string              `json:"parent_entity_id,omitempty"`
}

type UserMessagingSendResponse struct {
	// On success: suppressed=false, id=<uuid>.
	// On silent-mode suppression: suppressed=true, id=null, reason="silent".
	ID         *string `json:"id"`
	Suppressed bool    `json:"suppressed"`
	Reason     string  `json:"reason,omitempty"`
	Agency     string  `json:"agency,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type UserMessagingListResponse struct {
	Items []UserMessage `json:"items"`
	Count int           `json:"count"`
	Error string        `json:"error,omitempty"`
}

type UserMessagingRemindResponse struct {
	Ok             bool    `json:"ok"`
	ReminderCount  int     `json:"reminderCount"`
	NextReminderAt *string `json:"nextReminderAt"`
	Error          string  `json:"error,omitempty"`
}

type UserMessagingAgencyInfo struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Oversight   string `json:"oversight"` // silent | report | approval
	Fixed       bool   `json:"fixed"`
}

type UserMessagingAgenciesResponse struct {
	Items []UserMessagingAgencyInfo `json:"items"`
	Error string                    `json:"error,omitempty"`
}
