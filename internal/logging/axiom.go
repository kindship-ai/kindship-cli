package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Logger sends structured logs to Axiom
type Logger struct {
	token    string
	dataset  string
	client   *http.Client
	buffer   []LogEntry
	mu       sync.Mutex
	agentID  string
	command  string
	verbose  bool
}

// LogEntry is a structured log entry for Axiom
type LogEntry struct {
	Timestamp  time.Time `json:"_time"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
	AgentID    string    `json:"agent_id,omitempty"`
	Command    string    `json:"command,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	Component  string    `json:"component"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

var (
	globalLogger *Logger
	once         sync.Once
)

// Init initializes the global logger
func Init(agentID, command string, verbose bool) *Logger {
	once.Do(func() {
		token := os.Getenv("AXIOM_TOKEN")
		dataset := os.Getenv("AXIOM_DATASET")
		if dataset == "" {
			dataset = "kindship-logs"
		}

		globalLogger = &Logger{
			token:   token,
			dataset: dataset,
			client: &http.Client{
				Timeout: 5 * time.Second,
			},
			buffer:  make([]LogEntry, 0, 10),
			agentID: agentID,
			command: command,
			verbose: verbose,
		}
	})
	return globalLogger
}

// Get returns the global logger
func Get() *Logger {
	if globalLogger == nil {
		return &Logger{verbose: false}
	}
	return globalLogger
}

// IsEnabled returns true if Axiom logging is configured
func (l *Logger) IsEnabled() bool {
	return l.token != ""
}

// log adds an entry to the buffer
func (l *Logger) log(level, message string, extra map[string]interface{}) {
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   message,
		AgentID:   l.agentID,
		Command:   l.command,
		Component: "kindship-cli",
		Extra:     extra,
	}

	// Also print to stderr if verbose
	if l.verbose {
		fmt.Fprintf(os.Stderr, "[kindship:%s] %s\n", level, message)
	}

	if !l.IsEnabled() {
		return
	}

	l.mu.Lock()
	l.buffer = append(l.buffer, entry)
	l.mu.Unlock()
}

// Info logs an info message
func (l *Logger) Info(message string, extra ...map[string]interface{}) {
	var e map[string]interface{}
	if len(extra) > 0 {
		e = extra[0]
	}
	l.log("info", message, e)
}

// Error logs an error message
func (l *Logger) Error(message string, err error, extra ...map[string]interface{}) {
	e := make(map[string]interface{})
	if len(extra) > 0 {
		for k, v := range extra[0] {
			e[k] = v
		}
	}
	if err != nil {
		e["error"] = err.Error()
	}
	l.log("error", message, e)
}

// Warn logs a warning message
func (l *Logger) Warn(message string, extra ...map[string]interface{}) {
	var e map[string]interface{}
	if len(extra) > 0 {
		e = extra[0]
	}
	l.log("warn", message, e)
}

// Debug logs a debug message (only if verbose)
func (l *Logger) Debug(message string, extra ...map[string]interface{}) {
	if !l.verbose {
		return
	}
	var e map[string]interface{}
	if len(extra) > 0 {
		e = extra[0]
	}
	l.log("debug", message, e)
}

// WithDuration logs a message with duration
func (l *Logger) WithDuration(message string, duration time.Duration, extra ...map[string]interface{}) {
	e := make(map[string]interface{})
	if len(extra) > 0 {
		for k, v := range extra[0] {
			e[k] = v
		}
	}
	e["duration_ms"] = duration.Milliseconds()
	l.log("info", message, e)
}

// Flush sends all buffered logs to Axiom
func (l *Logger) Flush() error {
	if !l.IsEnabled() {
		return nil
	}

	l.mu.Lock()
	if len(l.buffer) == 0 {
		l.mu.Unlock()
		return nil
	}
	entries := l.buffer
	l.buffer = make([]LogEntry, 0, 10)
	l.mu.Unlock()

	body, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("failed to marshal logs: %w", err)
	}

	// Use EU edge endpoint for ingest (dataset is in eu-central-1)
	url := fmt.Sprintf("https://eu-central-1.aws.edge.axiom.co/v1/ingest/%s", l.dataset)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send logs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("axiom returned status %d", resp.StatusCode)
	}

	return nil
}

// FlushSync flushes logs synchronously (for use before process exit)
func (l *Logger) FlushSync() {
	if err := l.Flush(); err != nil {
		if l.verbose {
			fmt.Fprintf(os.Stderr, "[kindship] Failed to flush logs to Axiom: %v\n", err)
		}
	}
}
