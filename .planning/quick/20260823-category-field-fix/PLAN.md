# Plan: Fix Category Field in session_ready Response

## Goal
Add `Category` field to `sessionReadyResponse` struct and populate it from `session.Category()` so frontend can use correct language for speech recognition.

## Problem
The `session_ready` control frame currently sends only `type`, `session_id`, and `words`, but not the `category` field. This prevents the frontend from using the correct language setting for speech recognition (e.g., `zh-CN` for Chinese dictation vs `en-US` for English dictation).

## Solution
1. Add `Category string` field to `sessionReadyResponse` struct in `cmd/service/handlers.go`
2. Populate the field with `Category: s.Category()` when constructing the response

## Files to Change
- `cmd/service/handlers.go` (2 locations)
  - Line 71-75: Add `Category string` to struct definition
  - Line 252-256: Add `Category: s.Category()` to response construction

## Testing
- Build service: `go build ./cmd/service/...`
- Verify no compilation errors
- Optional: Quick test to ensure category field is sent in session_ready frame