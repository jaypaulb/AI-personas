package atom

import (
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RetryConfig configures the retry behavior for operations
type RetryConfig struct {
	// InitialDelay is the delay before the first retry (default 1s)
	InitialDelay time.Duration
	// MaxDelay is the maximum delay between retries (default 32s)
	MaxDelay time.Duration
	// MaxAttempts is the maximum number of attempts (default 5)
	MaxAttempts int
	// JitterFactor is the fraction of delay to randomize (0.0-1.0, default 0.1)
	JitterFactor float64
	// OperationName is used for logging (optional)
	OperationName string
}

// DefaultRetryConfig returns a RetryConfig with sensible defaults
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		InitialDelay: 1 * time.Second,
		MaxDelay:     32 * time.Second,
		MaxAttempts:  5,
		JitterFactor: 0.1,
	}
}

// RetryResult contains information about a retry operation
type RetryResult struct {
	Attempts int
	LastErr  error
	Success  bool
}

// Retry executes fn with exponential backoff and jitter.
// It returns nil if fn succeeds, or the last error if all attempts fail.
func Retry(config RetryConfig, fn func() error) error {
	result := RetryWithResult(config, fn)
	return result.LastErr
}

// RetryWithResult executes fn with exponential backoff and returns detailed result
func RetryWithResult(config RetryConfig, fn func() error) RetryResult {
	// Apply defaults for zero values
	if config.InitialDelay == 0 {
		config.InitialDelay = 1 * time.Second
	}
	if config.MaxDelay == 0 {
		config.MaxDelay = 32 * time.Second
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 5
	}
	if config.JitterFactor == 0 {
		config.JitterFactor = 0.1
	}

	result := RetryResult{}

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		result.Attempts = attempt

		err := fn()
		if err == nil {
			result.Success = true
			result.LastErr = nil
			return result
		}

		result.LastErr = err

		if attempt == config.MaxAttempts {
			// No more retries
			if config.OperationName != "" {
				log.Printf("[retry] %s: all %d attempts failed, last error: %v",
					config.OperationName, config.MaxAttempts, err)
			}
			break
		}

		// Calculate delay with exponential backoff
		delay := CalculateBackoff(attempt, config.InitialDelay, config.MaxDelay, config.JitterFactor)

		if config.OperationName != "" {
			log.Printf("[retry] %s: attempt %d/%d failed (%v), retrying in %v",
				config.OperationName, attempt, config.MaxAttempts, err, delay)
		}

		time.Sleep(delay)
	}

	return result
}

// CalculateBackoff calculates the delay for a retry attempt using exponential backoff with jitter.
// attempt is 1-indexed (first retry is attempt 1).
func CalculateBackoff(attempt int, initialDelay, maxDelay time.Duration, jitterFactor float64) time.Duration {
	// Exponential backoff: delay = initialDelay * 2^(attempt-1)
	multiplier := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(initialDelay) * multiplier)

	// Cap at max delay
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter: random value between -jitter and +jitter
	if jitterFactor > 0 {
		jitter := float64(delay) * jitterFactor
		randomJitter := (rand.Float64()*2 - 1) * jitter // Random value in [-jitter, +jitter]
		delay = time.Duration(float64(delay) + randomJitter)
	}

	// Ensure delay is not negative
	if delay < 0 {
		delay = 0
	}

	return delay
}

// ParseRetryAfter parses the Retry-After header from an HTTP response.
// Returns the duration to wait, or 0 if the header is not present or invalid.
func ParseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return 0
	}

	// Try parsing as seconds (integer)
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date (RFC1123)
	if t, err := time.Parse(time.RFC1123, retryAfter); err == nil {
		duration := time.Until(t)
		if duration > 0 {
			return duration
		}
	}

	return 0
}

// IsRetryableStatusCode returns true if the HTTP status code indicates a transient error
// that should be retried.
func IsRetryableStatusCode(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests: // 429
		return true
	case http.StatusInternalServerError: // 500
		return true
	case http.StatusBadGateway: // 502
		return true
	case http.StatusServiceUnavailable: // 503
		return true
	case http.StatusGatewayTimeout: // 504
		return true
	default:
		return false
	}
}

// IsRateLimitError returns true if the status code indicates rate limiting (429)
func IsRateLimitError(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests
}

// IsServerError returns true if the status code indicates a server error (5xx)
func IsServerError(statusCode int) bool {
	return statusCode >= 500 && statusCode < 600
}
