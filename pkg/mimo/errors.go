package mimo

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the mimo package. Callers MUST detect these via
// errors.Is, never by string matching.
var (
	// ErrAPIKeyRequired is returned when no API key is provided
	ErrAPIKeyRequired = errors.New("API key is required")

	// ErrInvalidRequest is returned when the API request is malformed
	ErrInvalidRequest = errors.New("invalid API request")

	// ErrAuthentication is returned when authentication fails
	ErrAuthentication = errors.New("API authentication failed")

	// ErrRateLimit is returned when rate limit is exceeded
	ErrRateLimit = errors.New("rate limit exceeded")

	// ErrServiceUnavailable is returned when the service is temporarily unavailable
	ErrServiceUnavailable = errors.New("service temporarily unavailable")

	// ErrQuotaExceeded is returned when API quota is exceeded
	ErrQuotaExceeded = errors.New("API quota exceeded")

	// ErrInvalidAudioFormat is returned when audio format is not supported
	ErrInvalidAudioFormat = errors.New("invalid audio format")

	// ErrAudioTooShort is returned when audio input is too short
	ErrAudioTooShort = errors.New("audio input too short")

	// ErrAudioTooLong is returned when audio input exceeds maximum duration
	ErrAudioTooLong = errors.New("audio input too long")

	// ErrDecodingFailed is returned when audio decoding fails
	ErrDecodingFailed = errors.New("audio decoding failed")

	// ErrEncodingFailed is returned when audio encoding fails
	ErrEncodingFailed = errors.New("audio encoding failed")

	// ErrStreamInterrupted is returned when streaming is interrupted
	ErrStreamInterrupted = errors.New("stream interrupted")

	// ErrTimeout is returned when request times out
	ErrTimeout = errors.New("request timeout")

	// ErrNetworkError is returned for network-related failures
	ErrNetworkError = errors.New("network error")

	// ErrInvalidResponse is returned when API response is invalid
	ErrInvalidResponse = errors.New("invalid API response")

	// ErrContextCancelled is returned when context is cancelled
	ErrContextCancelled = errors.New("context cancelled")

	// ErrUnsupportedVoice is returned when voice is not available
	ErrUnsupportedVoice = errors.New("unsupported voice")

	// ErrModelNotAvailable is returned when model is not available
	ErrModelNotAvailable = errors.New("model not available")
)

// APIError represents an error response from the Mimo API
type APIError struct {
	// Code is the error code returned by the API
	Code string
	// Message is the error message
	Message string
	// Type is the error type (e.g., "invalid_request_error", "authentication_error")
	Type string
	// StatusCode is the HTTP status code
	StatusCode int
	// Err is the underlying error
	Err error
}

// Error implements the error interface
func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("API error: %s (code: %s, type: %s, status: %d): %v", e.Message, e.Code, e.Type, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("API error: %s (code: %s, type: %s, status: %d)", e.Message, e.Code, e.Type, e.StatusCode)
}

// Unwrap implements the errors.Unwrap interface
func (e *APIError) Unwrap() error {
	return e.Err
}

// Is implements error comparison for errors.Is
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrInvalidRequest:
		return e.Type == "invalid_request_error"
	case ErrAuthentication:
		return e.Type == "authentication_error"
	case ErrRateLimit, ErrQuotaExceeded:
		return e.Type == "rate_limit_error" || e.Code == "quota_exceeded"
	case ErrServiceUnavailable:
		return e.StatusCode >= 500 || e.Type == "service_unavailable"
	case ErrModelNotAvailable:
		return e.Code == "model_not_available"
	default:
		return false
	}
}

// NewAPIError creates a new APIError from components
func NewAPIError(code, message, errorType string, statusCode int, err error) *APIError {
	return &APIError{
		Code:       code,
		Message:    message,
		Type:       errorType,
		StatusCode: statusCode,
		Err:        err,
	}
}

// RetryableError indicates an error that can be retried
type RetryableError struct {
	Err error
	// RetryAfter is the suggested duration to wait before retrying
	RetryAfter int
}

// Error implements the error interface
func (r *RetryableError) Error() string {
	return fmt.Sprintf("retryable error (after %ds): %v", r.RetryAfter, r.Err)
}

// Unwrap implements the errors.Unwrap interface
func (r *RetryableError) Unwrap() error {
	return r.Err
}

// Is implements error comparison for errors.Is
func (r *RetryableError) Is(target error) bool {
	// RetryableError is retryable by nature
	return false
}

// NewRetryableError creates a new RetryableError
func NewRetryableError(err error, retryAfter int) *RetryableError {
	return &RetryableError{
		Err:        err,
		RetryAfter: retryAfter,
	}
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	if errors.Is(err, ErrRateLimit) || errors.Is(err, ErrServiceUnavailable) || errors.Is(err, ErrNetworkError) {
		return true
	}
	if _, ok := err.(*RetryableError); ok {
		return true
	}
	return false
}

// GetRetryDelay extracts the retry delay from a retryable error
func GetRetryDelay(err error) int {
	if retryable, ok := err.(*RetryableError); ok {
		return retryable.RetryAfter
	}
	return 0 // Default: no suggested delay
}