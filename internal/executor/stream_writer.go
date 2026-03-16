package executor

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

const (
	// flushInterval is how often to flush batched lines to the API.
	flushInterval = 500 * time.Millisecond
	// maxBatchSize is the max lines to buffer before flushing.
	maxBatchSize = 50
)

// LogSender is the interface for sending log lines to the API.
type LogSender func(lines []api.LogLine) error

// StreamWriter wraps an io.Writer, buffers lines (split on \n),
// assigns monotonic seq numbers, and sends batches to the API.
// It also writes through to the underlying buffer for completion payload.
type StreamWriter struct {
	stream   string // "stdout" or "stderr"
	sender   LogSender
	seq      *atomic.Int64 // shared across stdout+stderr to avoid UNIQUE(run_id,seq) collisions
	mu       sync.Mutex
	pending  []api.LogLine
	partial  strings.Builder // Partial line buffer (no \n yet)
	done     chan struct{}
	wg       sync.WaitGroup
	sendWg   sync.WaitGroup // tracks in-flight background sends
	closed   bool
	passthru *limitedWriter // Also write to underlying buffer
}

// NewStreamWriter creates a StreamWriter that sends log lines via the sender func.
// The seq counter is shared across stdout and stderr writers for the same run,
// ensuring unique seq values (the DB has UNIQUE(run_id, seq)).
// The passthru writer receives all bytes for the completion payload (backward compat).
func NewStreamWriter(stream string, sender LogSender, passthru *limitedWriter, seq *atomic.Int64) *StreamWriter {
	sw := &StreamWriter{
		stream:   stream,
		sender:   sender,
		seq:      seq,
		done:     make(chan struct{}),
		passthru: passthru,
	}

	sw.wg.Add(1)
	go sw.flushLoop()

	return sw
}

// Write implements io.Writer. Splits input on newlines and buffers lines.
func (sw *StreamWriter) Write(p []byte) (int, error) {
	// Always write through to passthru for completion payload
	if sw.passthru != nil {
		sw.passthru.Write(p)
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	data := string(p)
	for {
		idx := strings.IndexByte(data, '\n')
		if idx == -1 {
			// No newline — accumulate in partial buffer
			sw.partial.WriteString(data)
			break
		}

		// Complete line found
		line := sw.partial.String() + data[:idx]
		sw.partial.Reset()
		data = data[idx+1:]

		if line == "" {
			continue // skip empty lines
		}

		seq := sw.seq.Add(1)
		sw.pending = append(sw.pending, api.LogLine{
			Seq:     seq,
			Stream:  sw.stream,
			Content: line,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		})

		if len(sw.pending) >= maxBatchSize {
			sw.flushLocked()
		}
	}

	return len(p), nil
}

// Close flushes any remaining partial line and pending batch, then stops the flush loop.
func (sw *StreamWriter) Close() error {
	sw.mu.Lock()
	if sw.closed {
		sw.mu.Unlock()
		return nil
	}
	sw.closed = true

	// Flush any remaining partial line
	if sw.partial.Len() > 0 {
		seq := sw.seq.Add(1)
		sw.pending = append(sw.pending, api.LogLine{
			Seq:     seq,
			Stream:  sw.stream,
			Content: sw.partial.String(),
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		})
		sw.partial.Reset()
	}

	sw.flushLocked()
	sw.mu.Unlock()

	// Stop the flush loop
	close(sw.done)
	sw.wg.Wait()

	// Wait for all in-flight background sends to complete
	sw.sendWg.Wait()

	return nil
}

// flushLoop periodically flushes pending lines.
func (sw *StreamWriter) flushLoop() {
	defer sw.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sw.mu.Lock()
			sw.flushLocked()
			sw.mu.Unlock()
		case <-sw.done:
			return
		}
	}
}

// flushLocked sends pending lines to the API. Must be called with mu held.
func (sw *StreamWriter) flushLocked() {
	if len(sw.pending) == 0 {
		return
	}

	batch := make([]api.LogLine, len(sw.pending))
	copy(batch, sw.pending)
	sw.pending = sw.pending[:0]

	// Send in background to avoid blocking writes
	sw.sendWg.Add(1)
	go func() {
		defer sw.sendWg.Done()
		_ = sw.sender(batch) // Best-effort; errors logged by sender
	}()
}
