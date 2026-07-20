package infra

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// RecoveryMiddleware catches panics raised by next, logs them at Error level
// with a stack trace, and writes HTTP 500 to the client. It does NOT mask
// errors in non-panic paths — when next returns normally the response passes
// through unchanged.
//
// Threats mitigated:
//   - T-01-10 Information Disclosure: the response body is a generic
//     "internal server error" string. The panic value and stack trace are
//     sent only to the logger (operator-controlled sink), never to the
//     client.
//   - D-49 panic recovery: required from the start of every HTTP handler
//     chain so a single buggy handler cannot crash the whole process.
//   - D-25 context propagation: the recovered-panic event inherits any
//     fields the caller pre-populated on the logger via With(...) (e.g.
//     request_id, component, operation), letting operators correlate the
//     500 with the originating request.
//
// Dependencies are injected (D-42) — the caller owns the logger, no package
// global is consulted. The middleware does not retain the request object
// past next.ServeHTTP's return, so it is safe to use concurrently.
func RecoveryMiddleware(logger Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// Build the structured fields for the recovered-panic event.
			// recovered_panic=true lets log queries distinguish real
			// panics from other Error-level entries; error carries the
			// panic value as a string so it survives JSON encoding
			// regardless of the panic value's dynamic type (string,
			// error, runtime.Error, or arbitrary value); stack gives
			// operators the goroutine state at the panic site.
			panicValue := fmt.Sprintf("%v", rec)
			stackTrace := string(debug.Stack())

			logger.Error("recovered panic",
				F("recovered_panic", true),
				F("error", panicValue),
				F("stack", stackTrace),
				F("method", r.Method),
				F("path", r.URL.Path),
			)

			// Generic body — DO NOT echo panicValue or stackTrace here.
			// Doing so would leak internal state to the client (T-01-10).
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}()

		next.ServeHTTP(w, r)
	})
}
