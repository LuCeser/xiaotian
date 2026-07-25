#!/bin/sh
# fake_prege.sh -- POSIX shell fake for pregenerate_dictation.py.
#
# Purpose (D-48 manual fake, shell variant): lets the bridge's pregenerate
# parsing / flock-contention / error-classification logic be exercised on
# Windows dev hosts that have no Python venv and no fcntl support. The fake
# is invoked INSTEAD of the real pregenerate_dictation.py by pointing the
# bridge at this script. It emits the same stdout/stderr contract the real
# script uses (RESEARCH Pattern 3 / Pitfall 3 / Pitfall 6):
#
#   - default            -> 3 mp3 paths on stdout (one per line, NOT JSON) +
#                           progress diagnostics on stderr, exit 0
#   - --lock-test        -> empty stdout + "Another instance running" on
#                           stderr, exit 0 (simulates fcntl.flock contention
#                           -- RESEARCH Pitfall 6 signature). The bridge's
#                           RunPregenerate detects this via the
#                           classifyPregenResult empty-stdout guard and
#                           returns ErrPythonEmpty.
#   - --fail             -> "ERROR: ..." on stderr, exit 1 (non-zero exit
#                           classification -> *ExitError wrapping
#                           ErrPythonExit)
#
# Portability: POSIX sh only. Run on git-bash (Windows) and dash/bash
# (Linux). Used by //go:build linux integration tests and by manual smoke
# checks on Windows; not part of `go test -short`.
#
# argv contract: the real pregenerate_dictation.py expects
#   generate --words '<jsonArray>' --language <lang> [--prefix p] [--voice v]
#           [--words-per-segment n]
# This fake IGNORES the argv contents -- it dispatches purely on the
# --lock-test / --fail sentinel flags (which the real script does NOT
# accept; they are fake-only). For the default path, it does not validate
# --words/--language; the bridge's pregenArgs test covers argv construction.
#
# This file is INTENTIONALLY test infrastructure, not production code.

set -u

# Defaults -- the happy path.
emit_paths=1
emit_error=0

# Walk argv looking for fake-only sentinel flags. We must NOT echo anything
# until we have decided which path to take (so --lock-test produces truly
# empty stdout, matching the real script's Pitfall 6 signature).
for arg in "$@"; do
    case "$arg" in
        --lock-test)
            emit_paths=0
            ;;
        --fail)
            emit_error=1
            ;;
    esac
done

# Error path takes precedence over the lock-test path so tests can drive the
# non-zero-exit branch deterministically.
if [ "$emit_error" -eq 1 ]; then
    echo "ERROR: simulated pregenerate failure (fake_prege.sh --fail)" >&2
    exit 1
fi

if [ "$emit_paths" -eq 0 ]; then
    # RESEARCH Pitfall 6 signature: diagnostic on stderr + EMPTY stdout +
    # exit 0. The real script does this when fcntl.flock(LOCK_EX|LOCK_NB)
    # fails because another instance holds the lock
    # (pregenerate_dictation.py:1303-1307). The bridge's classifyPregenResult
    # detects this and returns ErrPythonEmpty rather than a silent zero-path
    # success.
    echo "Another pregenerate_dictation.py instance is running; exiting." >&2
    exit 0
fi

# Happy path: progress logs to stderr (mirrors pregenerate_dictation.py:624,
# 715, 907, 1069 etc.), mp3 absolute paths to stdout one per line
# (mirrors pregenerate_dictation.py:1147-1148).
echo "Intros: generating 3 files" >&2
echo "Concat: wrote 1 segment" >&2
echo "/opt/hta/cache/hello.mp3"
echo "/opt/hta/cache/goodbye.mp3"
echo "/opt/hta/cache/thank_you.mp3"
exit 0
