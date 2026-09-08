// Package ocr implements the photo-answer OCR pipeline for the dictation
// tutor (Ticket 04): validating a reassembled photo as JPEG, reading the
// handwritten answer through a vision-API seam, and parsing the model reply
// into text plus a confidence score. Confidence below the caller's threshold
// reports ErrLowConfidence alongside the best guess so the client can offer
// the manual-input fallback prefilled.
//
// The package deliberately has no dependency on pkg/mimo: the pipeline takes
// a Reader func seam and the handler layer adapts the mimo client's
// ChatVision call to it, mirroring the Transcriber/Chatter seam pattern of
// pkg/voice (Ticket 03, D-05).
package ocr
