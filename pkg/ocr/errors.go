package ocr

import "errors"

// Sentinel errors for the OCR pipeline. Callers MUST detect them via
// errors.Is, never by string matching (mirrors the voice/mimo conventions).
var (
	// ErrEmptyImage is returned by Pipeline.Process when no image bytes
	// arrived (dropped frames, empty upload) — surfaced instead of a wasted
	// vision API round trip.
	ErrEmptyImage = errors.New("ocr: empty image")

	// ErrNotJPEG is returned by Pipeline.Process when the reassembled bytes
	// lack the JPEG SOI signature. The client only ever ships JPEG (the web
	// controller re-encodes through a canvas), so anything else means a
	// corrupted or hostile upload.
	ErrNotJPEG = errors.New("ocr: image is not a JPEG")

	// ErrNotImage is returned by WordlistPipeline.Recognize when the bytes
	// are neither a PNG nor a JPEG. Unlike the student-answer pipeline
	// (JPEG-only because the web controller re-encodes), the parent word-list
	// upload accepts small PNGs verbatim, so both signatures are sniffed.
	ErrNotImage = errors.New("ocr: bytes are not a PNG or JPEG image")

	// ErrImageTooLarge is returned by WordlistPipeline.Recognize when the
	// upload exceeds the pipeline's byte cap — before any vision-API round
	// trip, so an oversize photo costs nothing.
	ErrImageTooLarge = errors.New("ocr: image exceeds the size limit")

	// ErrTooManyEntries is returned by WordlistPipeline.Recognize when the
	// model reply carries more entries than the commit path can accept in
	// one request (Issue #3's single-photo limit mirrors maxEntries).
	ErrTooManyEntries = errors.New("ocr: too many word-list entries")

	// ErrTooManyPoems is the Issue #11 poems-side twin of ErrTooManyEntries:
	// the mixed-page reply carries more whole poems than the commit path
	// accepts in one request (maxWordlistPoems).
	ErrTooManyPoems = errors.New("ocr: too many poems in one recognition")

	// ErrEmptyWords is returned by MeaningGenerator.Generate when the
	// supplement request carries no usable words after trim/dedup — the
	// HTTP layer maps it to a 400 rather than spending an LLM round trip
	// on nothing (Issue #8).
	ErrEmptyWords = errors.New("ocr: no words to supplement")

	// ErrEmptyText is returned when the model reports no recognizable
	// handwriting (blank photo, unreadable scrawl) — the client offers the
	// manual-input fallback without a prefilled guess.
	ErrEmptyText = errors.New("ocr: no recognizable text in image")

	// ErrLowConfidence rides alongside the best-guess Result when the model's
	// confidence is below the caller's threshold. The Result is still
	// meaningful (prefill the manual input), so callers read both values.
	ErrLowConfidence = errors.New("ocr: confidence below threshold")

	// errUnparseableReply is returned when the model reply contains no
	// extractable OCR JSON. Informational: the caller surfaces the manual
	// fallback.
	errUnparseableReply = errors.New("ocr: unparseable model reply")

	// errReaderFailed wraps a vision-API transport/API error from the Reader
	// seam.
	errReaderFailed = errors.New("ocr: vision reader failed")

	// errNilSeam is returned by Pipeline.Process when the pipeline was built
	// without a Reader (wiring bug).
	errNilSeam = errors.New("ocr: pipeline has a nil seam")
)
