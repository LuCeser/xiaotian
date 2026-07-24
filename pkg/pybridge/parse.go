package pybridge

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/pkg/errors"
)

// extractTrailingJSON locates and returns the LAST complete top-level JSON
// value in stdout as a json.RawMessage.
//
// Why this exists (RESEARCH Pattern 2 / Pitfall 2): vocab_manager.py
// mixes success JSON, "Warning:" lines, and "Error:" lines on stdout --
// its review subcommand emits warning lines BEFORE the trailing JSON
// object in the same stream. A naive json.Unmarshal(stdout, ...) fails
// on the prefix text. The bridge instead calls extractTrailingJSON to
// peel off the trailing JSON value, then unmarshals that.
//
// Strategy (tail backward scan + bracket stack, per 03-02-PLAN.md):
//  1. TrimRight trailing whitespace.
//  2. Walk backward past any trailing non-structural bytes to find the
//     closing '}' or ']' of the trailing JSON value.
//  3. From that closer, walk backward with a depth counter (depth++ on
//     every '}' or ']', depth-- on every '{' or '['); when depth hits
//     0 we have found the matching opener. Brackets inside JSON string
//     literals are ignored by tracking string-literal state.
//
// String-literal / escape handling: while inside a string, the only byte
// that can exit it is an unescaped '"'. Because we walk backward, a '"'
// is the real string terminator iff the run of consecutive '\' bytes
// immediately preceding it is even (0, 2, 4 ...). An odd run means the
// '"' is escaped content and stays inside the string.
//
// The function is a pure transform with no I/O -- testable with golden
// fixtures and no Python interpreter. Callers MUST still run
// json.Unmarshal on the returned RawMessage; this function only locates
// the bytes, it does not validate they are semantically valid JSON
// (though for vocab_manager output they always are).
func extractTrailingJSON(stdout []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimRight(stdout, " \t\r\n\v\f")
	if len(trimmed) == 0 {
		return nil, errors.Wrap(ErrPythonJSONParse, "empty stdout")
	}

	// Locate the closing bracket of the trailing JSON value. Walk
	// backward past any non-structural trailing bytes; vocab_manager
	// output never has trailing junk after the JSON, but the trim is
	// cheap insurance.
	end := len(trimmed) - 1
	for end >= 0 && trimmed[end] != '}' && trimmed[end] != ']' {
		end--
	}
	if end < 0 {
		return nil, errors.Wrap(ErrPythonJSONParse,
			"no closing '}' or ']' in stdout")
	}

	// Walk backward to the matching opener, tracking string-literal
	// state so brackets inside JSON strings do not affect depth.
	depth := 0
	inString := false
	for i := end; i >= 0; i-- {
		c := trimmed[i]
		if inString {
			if c == '"' {
				// Count the run of consecutive '\' immediately
				// preceding this '"'. Even run => real terminator
				// (exit the string going backward); odd run => the
				// '"' is escaped content.
				bs := 0
				for j := i - 1; j >= 0 && trimmed[j] == '\\'; j-- {
					bs++
				}
				if bs%2 == 0 {
					inString = false
				}
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '}', ']':
			depth++
		case '{', '[':
			depth--
			if depth == 0 {
				return json.RawMessage(trimmed[i : end+1]), nil
			}
		}
	}
	return nil, errors.Wrap(ErrPythonJSONParse,
		"no matching opening bracket for trailing JSON value")
}

// splitNonEmptyLines splits stdout into trimmed non-empty lines.
//
// Why this exists (RESEARCH Pattern 3 / Pitfall 3):
// pregenerate_dictation.py's generate subcommand prints one mp3 absolute
// path per line to stdout (NOT JSON -- it prints `os.path.abspath(p)`
// for each generated file). The bridge parses these into
// PregenResult.Paths with this helper. stderr is separately captured for
// logging.
//
// Whitespace-only lines (including the trailing newline) are dropped.
// Each returned line is TrimSpace'd so '\r\n' line endings do not leak
// a trailing '\r' into path strings on hosts where the Python process
// emits CRLF.
//
// Path validation is intentionally NOT done here -- the bridge layer
// (Plan 03-05) is responsible for confining paths to the configured
// CacheDir (T-03-06 tampering mitigation). This function only splits.
func splitNonEmptyLines(stdout []byte) []string {
	lines := strings.Split(string(stdout), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
