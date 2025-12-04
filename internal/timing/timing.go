// Package timing provides utilities for measuring and logging operation durations.
// Logging is conditional on the DEBUG environment variable.
package timing

import (
	"log"
	"os"
	"time"
)

// debugEnabled caches the DEBUG environment variable check at package init time.
var debugEnabled bool

func init() {
	debugEnabled = os.Getenv("DEBUG") == "1"
}

// IsDebugEnabled returns whether DEBUG=1 is set.
func IsDebugEnabled() bool {
	return debugEnabled
}

// Timer measures elapsed time for an operation.
type Timer struct {
	name    string
	start   time.Time
	stopped bool
	end     time.Time
}

// Start creates and starts a new Timer with the given operation name.
func Start(name string) *Timer {
	return &Timer{
		name:  name,
		start: time.Now(),
	}
}

// Stop stops the timer and records the end time.
// Calling Stop multiple times has no effect after the first call.
func (t *Timer) Stop() {
	if !t.stopped {
		t.end = time.Now()
		t.stopped = true
	}
}

// Duration returns the elapsed time.
// If the timer has been stopped, it returns the time between start and stop.
// If the timer is still running, it returns the time since start.
func (t *Timer) Duration() time.Duration {
	if t.stopped {
		return t.end.Sub(t.start)
	}
	return time.Since(t.start)
}

// Name returns the operation name associated with this timer.
func (t *Timer) Name() string {
	return t.name
}

// StopAndLog stops the timer and logs the result if DEBUG is enabled.
// Returns the duration for convenience.
func (t *Timer) StopAndLog(success bool) time.Duration {
	t.Stop()
	LogOperation(t.name, t.Duration(), success)
	return t.Duration()
}

// LogOperation logs timing information in a structured format.
// Only logs if DEBUG=1 is set in the environment.
// Format: [timing] operation=%s duration_ms=%d success=%t
func LogOperation(name string, duration time.Duration, success bool) {
	if !debugEnabled {
		return
	}
	log.Printf("[timing] operation=%s duration_ms=%d success=%t", name, duration.Milliseconds(), success)
}

// LogOperationWithDetails logs timing information with additional details.
// Only logs if DEBUG=1 is set in the environment.
// Format: [timing] operation=%s duration_ms=%d success=%t %s
func LogOperationWithDetails(name string, duration time.Duration, success bool, details string) {
	if !debugEnabled {
		return
	}
	log.Printf("[timing] operation=%s duration_ms=%d success=%t %s", name, duration.Milliseconds(), success, details)
}
