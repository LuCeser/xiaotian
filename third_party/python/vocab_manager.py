#!/usr/bin/env python3
"""
Vocabulary Manager with FSRS spaced repetition.
Designed for English and Chinese dictation practice with FSRS scheduling.

Usage:
  vocab_manager.py add --unit "Unit3" --words '{"apple":"苹果","banana":"香蕉"}'
  vocab_manager.py add --unit "Unit3" --file words.json
  vocab_manager.py due [--unit UNIT] [--limit N]
  vocab_manager.py new [--unit UNIT] [--limit N]
  vocab_manager.py review --results '{"word1":"good","word2":"again"}'
  vocab_manager.py stats
  vocab_manager.py list [--unit UNIT]
  vocab_manager.py export [--category CATEGORY]
"""

import argparse
import json
import os
import sqlite3
import sys
from datetime import datetime, timezone, timedelta


# Same ceiling as Go pybridge.MaxDueLimit / FSRS DefaultWordCount [1, 100].
MAX_CARD_LIMIT = 100
# Ten 366-day years. Must match Go pybridge.MaxFetchLockHours.
MAX_FETCH_LOCK_HOURS = 24 * 366 * 10


def positive_int(value):
    """Argparse type: reject 0 and negative numbers."""
    iv = int(value)
    if iv <= 0:
        raise argparse.ArgumentTypeError(f"value must be a positive integer, got {value}")
    return iv


def bounded_positive_int(maximum):
    """Argparse type factory: positive integer no larger than maximum."""

    def parse(value):
        iv = positive_int(value)
        if iv > maximum:
            raise argparse.ArgumentTypeError(
                f"value must be <= {maximum}, got {value}"
            )
        return iv

    return parse

# FSRS imports
from fsrs import Scheduler, Card, Rating, State

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
SKILL_DIR = os.path.dirname(SCRIPT_DIR)


def _load_env():
    candidates = []
    if os.environ.get("DICTATION_ENV_FILE"):
        candidates.append(os.environ["DICTATION_ENV_FILE"])
    candidates.extend([
        os.environ.get("DICTATION_ENV_FILE", ""),
        os.path.join(SKILL_DIR, ".env"),
        os.path.join(os.getcwd(), ".env"),
    ])
    seen = set()
    while candidates:
        env_path = candidates.pop(0)
        if not env_path or env_path in seen:
            continue
        seen.add(env_path)
        if os.path.exists(env_path):
            with open(env_path, encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if line and not line.startswith("#") and "=" in line:
                        k, v = line.split("=", 1)
                        os.environ.setdefault(k.strip(), v.strip())
            nested = os.environ.get("DICTATION_ENV_FILE")
            if nested and nested not in seen:
                candidates.append(nested)


_load_env()

DEFAULT_DATA_DIR = os.environ.get("DICTATION_DATA_DIR", os.path.join(SKILL_DIR, "data"))
DB_PATH = os.environ.get("DICTATION_DB_PATH", os.path.join(DEFAULT_DATA_DIR, "vocab.db"))

# Shanghai timezone
CST = timezone(timedelta(hours=8))

# Scheduler tuned for a young student (D-13/D-14): only TWO knobs are
# configurable — desired_retention and maximum_interval, read at startup from
# DEFAULT_DATA_DIR/fsrs_config.json. Learning/relearning steps and fuzzing
# stay hard-coded.
#
# Single-resolution rule (Pitfall 8): DEFAULT_DATA_DIR resolves
# DICTATION_DATA_DIR first (below), the same env Bridge.childEnv forwards to
# every subprocess (bridge.go:208-210). The Go writer side is
# pkg/web/api (Plan 09-05), which writes cfg.Python.DataDir/fsrs_config.json
# atomically (temp file + rename) — writer and reader land on ONE file.
#
# Warn-and-fallback (Pitfall 9): an absent file is SILENT (defaults are not
# an error); malformed JSON, missing/wrong-typed keys, or out-of-range values
# print a "Warning:" line to stdout and fall back to the tuned defaults —
# no loader failure may ever propagate into command execution, or a corrupted
# config would take down the student dictation path (due/review included).
# The bridge tolerates Warning lines before the command JSON
# (extractTrailingJSON). Range checks live here because the fsrs library
# validates only its 21 model weights, not these kwargs.
_DEFAULT_DESIRED_RETENTION = 0.85
_DEFAULT_MAXIMUM_INTERVAL = 180


def load_scheduler():
    """Build the FSRS Scheduler from DataDir/fsrs_config.json (D-13/D-14)."""
    config_path = os.path.join(DEFAULT_DATA_DIR, "fsrs_config.json")
    desired_retention = _DEFAULT_DESIRED_RETENTION
    maximum_interval = _DEFAULT_MAXIMUM_INTERVAL
    if os.path.exists(config_path):
        try:
            with open(config_path, encoding="utf-8") as f:
                cfg = json.load(f)
            if not isinstance(cfg, dict):
                raise TypeError(f"config must be a JSON object, got {type(cfg).__name__}")
            dr = cfg["desired_retention"]
            mi = cfg["maximum_interval"]
            if isinstance(dr, bool) or not isinstance(dr, (int, float)) or not (0.7 <= dr <= 0.95):
                raise ValueError(f"desired_retention must be a number in [0.7, 0.95], got {dr!r}")
            if isinstance(mi, bool) or not isinstance(mi, int) or not (1 <= mi <= 36500):
                raise ValueError(f"maximum_interval must be a positive int <= 36500, got {mi!r}")
            desired_retention = float(dr)
            maximum_interval = mi
        except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as e:
            print(
                f"Warning: ignoring invalid fsrs config {config_path}: {e}; "
                f"using defaults (desired_retention={_DEFAULT_DESIRED_RETENTION}, "
                f"maximum_interval={_DEFAULT_MAXIMUM_INTERVAL})"
            )
    return Scheduler(
        desired_retention=desired_retention,
        learning_steps=(timedelta(minutes=1), timedelta(minutes=10)),
        relearning_steps=(timedelta(minutes=5),),
        maximum_interval=maximum_interval,
        enable_fuzzing=False,
    )


SCHEDULER = load_scheduler()


def get_db(db_path=DB_PATH):
    # Fix: honor the --db CLI option instead of always opening the hard-coded default.
    db_dir = os.path.dirname(os.path.abspath(db_path))
    if db_dir:
        os.makedirs(db_dir, exist_ok=True)
    db = sqlite3.connect(db_path)
    db.row_factory = sqlite3.Row
    db.execute("PRAGMA journal_mode=WAL")
    return db


FETCH_LOCK_HOURS = 24  # Default lock duration; configurable via --fetch-lock-hours


def init_db(db):
    db.executescript("""
        CREATE TABLE IF NOT EXISTS cards (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            word TEXT NOT NULL,
            meaning TEXT NOT NULL DEFAULT '',
            example TEXT NOT NULL DEFAULT '',
            unit TEXT NOT NULL DEFAULT '',
            category TEXT NOT NULL DEFAULT 'english',
            fsrs_card TEXT NOT NULL DEFAULT '',
            participates INTEGER NOT NULL DEFAULT 1 CHECK (participates IN (0, 1)),
            created_at TEXT NOT NULL DEFAULT (datetime('now')),
            UNIQUE(word, category)
        );
        CREATE TABLE IF NOT EXISTS review_logs (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            card_id INTEGER NOT NULL,
            rating INTEGER NOT NULL,
            reviewed_at TEXT NOT NULL DEFAULT (datetime('now')),
            FOREIGN KEY (card_id) REFERENCES cards(id)
        );
        CREATE INDEX IF NOT EXISTS idx_cards_unit ON cards(unit);
        CREATE INDEX IF NOT EXISTS idx_cards_category ON cards(category);
        CREATE INDEX IF NOT EXISTS idx_logs_card ON review_logs(card_id);
        -- Issue #4: textbook attribution + learned progress. lessons is a
        -- JSON array of lesson names in EXPLICIT teaching order (never
        -- dictionary order); learned_lessons N means 已学完第 N 课 (lesson
        -- N included, N+1 excluded). Descriptive metadata (edition/stage/
        -- grade/volume/module) is all optional — a textbook need not fill
        -- every level.
        CREATE TABLE IF NOT EXISTS textbooks (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            category TEXT NOT NULL DEFAULT 'english',
            edition TEXT NOT NULL DEFAULT '',
            stage TEXT NOT NULL DEFAULT '',
            grade TEXT NOT NULL DEFAULT '',
            volume TEXT NOT NULL DEFAULT '',
            module TEXT NOT NULL DEFAULT '',
            lessons TEXT NOT NULL DEFAULT '[]',
            learned_lessons INTEGER NOT NULL DEFAULT 0 CHECK (learned_lessons >= 0),
            is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
            created_at TEXT NOT NULL DEFAULT (datetime('now')),
            UNIQUE(name, category)
        );
        -- One lesson per (card, textbook) for now — multi-source
        -- attribution is issue #6's US-32, not built ahead of need.
        -- lesson = '' means 待确认 (lesson unknown): never counts as
        -- learned. include_in_scope = 1 is the parent's explicit
        -- "include regardless of lesson progress" override.
        CREATE TABLE IF NOT EXISTS card_assignments (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            card_id INTEGER NOT NULL,
            textbook_id INTEGER NOT NULL,
            lesson TEXT NOT NULL DEFAULT '',
            include_in_scope INTEGER NOT NULL DEFAULT 0 CHECK (include_in_scope IN (0, 1)),
            created_at TEXT NOT NULL DEFAULT (datetime('now')),
            UNIQUE(card_id, textbook_id)
        );
        CREATE INDEX IF NOT EXISTS idx_assignments_card ON card_assignments(card_id);
        CREATE INDEX IF NOT EXISTS idx_assignments_textbook ON card_assignments(textbook_id);
    """)
    # Migration: add fetch_locked_until column if missing
    cols = [row[1] for row in db.execute("PRAGMA table_info(cards)").fetchall()]
    if "fetch_locked_until" not in cols:
        db.execute("ALTER TABLE cards ADD COLUMN fetch_locked_until TEXT DEFAULT NULL")
        db.execute(
            "CREATE INDEX IF NOT EXISTS idx_cards_fetch_lock ON cards(category, fetch_locked_until)"
        )
    if "participates" not in cols:
        db.execute("ALTER TABLE cards ADD COLUMN participates INTEGER NOT NULL DEFAULT 1 CHECK (participates IN (0, 1))")
    db.execute("CREATE INDEX IF NOT EXISTS idx_cards_participates ON cards(category, participates)")
    # Issue #8 / ADR-0005: meaning provenance. 'material' = 材料原文或词库
    # 既有释义（历史数据一律如此）; 'generated' = 系统按教材语境补充（家长
    # 可能已改写，仍非教材原文）。展示规则 = meaning 非空且 source=
    # 'generated'; 编辑（cmd_edit）不改来源。
    if "meaning_source" not in cols:
        db.execute(
            "ALTER TABLE cards ADD COLUMN meaning_source TEXT NOT NULL DEFAULT 'material' "
            "CHECK (meaning_source IN ('material', 'generated'))"
        )
    db.commit()


def has_textbook_tables(db):
    """True once the Issue #4 tables exist. Pre-migration databases (opened
    by an older binary before any Python command ran) simply have no scope
    state: every card stays supplementary and eligible, matching the
    participates-column degraded-read precedent on the Go side."""
    return db.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'card_assignments'"
    ).fetchone() is not None


def scope_predicate(db):
    """(sql, params) implementing the Issue #4 textbook-scope eligibility.

    A card is IN scope iff it has NO textbook assignment (supplementary
    vocabulary — issue #2's participation switch alone governs it), or one
    of its assignments carries include_in_scope=1, or one of its
    assignments names a lesson inside its textbook's FIRST
    learned_lessons lessons (已学完第 N 课 includes lesson N). 未知课次
    (lesson = '') is never learned. Allowed lesson sets are computed here
    in Python — no SQLite JSON1 dependency — mirroring the Go side in
    pkg/database/card_repo.go so both selection choke points stay the
    same shape.
    """
    branches = []
    params = []
    rows = db.execute(
        "SELECT id, lessons, learned_lessons FROM textbooks WHERE learned_lessons > 0"
    ).fetchall()
    for row in rows:
        try:
            lessons = json.loads(row["lessons"])
        except (TypeError, ValueError):
            lessons = []
        allowed = [n for n in lessons[: row["learned_lessons"]] if isinstance(n, str) and n]
        if not allowed:
            continue
        placeholders = ",".join("?" for _ in allowed)
        branches.append(f"(la.textbook_id = ? AND la.lesson IN ({placeholders}))")
        params.append(row["id"])
        params.extend(allowed)
    learned_sql = " OR ".join(branches) if branches else "0"
    sql = (
        " AND (\n"
        "  NOT EXISTS (SELECT 1 FROM card_assignments na WHERE na.card_id = c.id)\n"
        "  OR EXISTS (SELECT 1 FROM card_assignments ia WHERE ia.card_id = c.id AND ia.include_in_scope = 1)\n"
        f"  OR EXISTS (SELECT 1 FROM card_assignments la WHERE la.card_id = c.id AND ({learned_sql}))\n"
        " )"
    )
    return sql, params


def normalize_meaning_source(source, meaning):
    """Issue #8 / ADR-0005: collapse a wire meaning_source onto the stored
    enum. Only 'generated' is distinguishable, and only with a non-empty
    meaning — an empty gloss never carries a marker (cancelled supplements
    and untouched missing rows both store 'material')."""
    if not meaning:
        return "material"
    return "generated" if source == "generated" else "material"


def cmd_add(db, args):
    """Add words to the vocabulary database."""
    if args.file:
        try:
            with open(args.file, "r", encoding="utf-8") as f:
                data = json.load(f)
        except FileNotFoundError:
            print(f"Error: file not found: {args.file}")
            sys.exit(1)
        except json.JSONDecodeError as e:
            print(f"Error: invalid JSON in file: {e}")
            sys.exit(1)
    elif args.words:
        try:
            data = json.loads(args.words)
        except json.JSONDecodeError as e:
            print(f"Error: invalid --words JSON: {e}")
            sys.exit(1)
    else:
        print("Error: provide --words or --file")
        sys.exit(1)

    # data can be:
    # {"word": "meaning"} or [{"word":"apple","meaning":"苹果","example":"..."},...]
    entries = []
    if isinstance(data, dict):
        for word, meaning in data.items():
            if not isinstance(word, str) or not word.strip():
                print(f"Error: word must be a non-empty string, got: {word!r}")
                sys.exit(1)
            entries.append({"word": word.strip(), "meaning": str(meaning) if meaning else "", "example": ""})
    elif isinstance(data, list):
        for i, item in enumerate(data):
            if not isinstance(item, dict):
                print(f"Error: item {i} is not a JSON object: {type(item).__name__}")
                sys.exit(1)
            word = item.get("word", "")
            if not isinstance(word, str) or not word.strip():
                print(f"Error: item {i} missing or empty 'word' field")
                sys.exit(1)
            entries.append({
                "word": word.strip(),
                "meaning": str(item.get("meaning", "")) if item.get("meaning") else "",
                "example": str(item.get("example", "")) if item.get("example") else "",
                "meaning_source": normalize_meaning_source(item.get("meaning_source"), item.get("meaning")),
            })
    else:
        print("Error: data must be a JSON object or array")
        sys.exit(1)

    # Create a new FSRS card for each
    added = 0
    skipped = 0
    for entry in entries:
        fsrs_card = Card()
        try:
            db.execute(
                "INSERT INTO cards (word, meaning, example, unit, category, fsrs_card, meaning_source) VALUES (?, ?, ?, ?, ?, ?, ?)",
                (
                    entry["word"],
                    entry.get("meaning", ""),
                    entry.get("example", ""),
                    args.unit or "",
                    args.category or "english",
                    fsrs_card.to_json(),
                    entry.get("meaning_source", "material"),
                ),
            )
            added += 1
        except sqlite3.IntegrityError:
            skipped += 1

    db.commit()
    print(json.dumps({"added": added, "skipped": skipped}, ensure_ascii=False))


def cmd_due(db, args):
    """Get cards that are due for review. Marks fetched cards with fetch_locked_until unless --peek."""
    now = datetime.now(timezone.utc)
    lock_hours = args.fetch_lock_hours if args.fetch_lock_hours is not None else FETCH_LOCK_HOURS
    peek = args.peek or False

    # Use BEGIN IMMEDIATE for atomic select + mark
    if not peek:
        db.execute("BEGIN IMMEDIATE")

    query = """
        SELECT c.id, c.word, c.meaning, c.example, c.unit, c.category, c.fsrs_card
        FROM cards c
        WHERE c.category = ? AND c.participates = 1
    """
    params = [args.category or "english"]

    # Issue #4: textbook scope rides WITH participation (AND, never OR) —
    # a word enters automatic selection only when both switches admit it.
    if has_textbook_tables(db):
        scope_sql, scope_params = scope_predicate(db)
        query += scope_sql
        params.extend(scope_params)

    if args.unit:
        query += " AND c.unit = ?"
        params.append(args.unit)
    elif args.units:
        units = [u.strip() for u in args.units.split(",") if u.strip()]
        if units:
            placeholders = ",".join(["?"] * len(units))
            query += f" AND c.unit IN ({placeholders})"
            params.extend(units)

    # Exclude fetch-locked cards (unless peeking)
    if not peek:
        query += " AND (c.fetch_locked_until IS NULL OR c.fetch_locked_until <= ?)"
        params.append(now.strftime("%Y-%m-%d %H:%M:%S"))

    if args.exclude:
        exclude_words = [w.strip() for w in args.exclude.split(",") if w.strip()]
        if exclude_words:
            placeholders = ",".join(["?"] * len(exclude_words))
            query += f" AND c.word NOT IN ({placeholders})"
            params.extend(exclude_words)

    rows = db.execute(query, params).fetchall()

    due_cards = []
    for row in rows:
        fsrs_card = Card.from_json(row["fsrs_card"])
        # Ensure fsrs_card.due is timezone-aware for comparison
        due = fsrs_card.due
        if due.tzinfo is None:
            due = due.replace(tzinfo=timezone.utc)
        # Card is due if its due date has passed (new cards have due=now, so always due)
        if due <= now:
            due_cards.append({
                "id": row["id"],
                "word": row["word"],
                "meaning": row["meaning"],
                "example": row["example"],
                "unit": row["unit"],
                "state": fsrs_card.state.name,
                "due": due.astimezone(CST).strftime("%Y-%m-%d %H:%M"),
                "stability": round(fsrs_card.stability, 2) if fsrs_card.stability else 0,
                "difficulty": round(fsrs_card.difficulty, 2) if fsrs_card.difficulty else 0,
            })

    # Sort: Learning state first, then by due date
    due_cards.sort(key=lambda x: (0 if x["state"] == "Learning" else 1, x["due"]))

    if args.limit is not None:
        due_cards = due_cards[: args.limit]

    # Mark fetched cards with lock timestamp (only when not peeking)
    if not peek and due_cards:
        locked_until = (now + timedelta(hours=lock_hours)).strftime("%Y-%m-%d %H:%M:%S")
        ids = [c["id"] for c in due_cards]
        placeholders = ",".join(["?"] * len(ids))
        db.execute(
            f"UPDATE cards SET fetch_locked_until = ? WHERE id IN ({placeholders})",
            [locked_until] + ids,
        )
        db.commit()

    if not peek:
        # Commit the BEGIN IMMEDIATE transaction if no cards were returned
        try:
            db.commit()
        except sqlite3.OperationalError:
            pass  # Already committed

    print(json.dumps(due_cards, ensure_ascii=False, indent=2))


def cmd_unlock(db, args):
    """Clear fetch locks on cards.

    --ids '[1,2,3]' unlocks exactly those cards (scoped per-card release —
    the session-exit path, so one student finishing cannot drop another
    student's in-flight locks). Without --ids, the category/unit filter
    applies (testing aid / admin sweep).
    """
    if args.ids:
        try:
            ids = json.loads(args.ids)
        except ValueError:
            print("Error: --ids must be a JSON array of integers")
            sys.exit(1)
        if not isinstance(ids, list) or not all(isinstance(i, int) and not isinstance(i, bool) for i in ids):
            print("Error: --ids must be a JSON array of integers")
            sys.exit(1)
        if not ids:
            print(json.dumps({"unlocked": 0, "ids": []}, ensure_ascii=False))
            return
        placeholders = ",".join("?" for _ in ids)
        cur = db.execute(
            f"UPDATE cards SET fetch_locked_until = NULL WHERE id IN ({placeholders})",
            ids,
        )
        db.commit()
        print(json.dumps({"unlocked": cur.rowcount, "ids": ids}, ensure_ascii=False))
        return

    query = "UPDATE cards SET fetch_locked_until = NULL WHERE category = ?"
    params = [args.category or "english"]
    if args.unit:
        query += " AND unit = ?"
        params.append(args.unit)

    cur = db.execute(query, params)
    db.commit()
    print(json.dumps({"unlocked": cur.rowcount, "category": args.category, "unit": args.unit}, ensure_ascii=False))


def cmd_new(db, args):
    """Get new cards (never reviewed, in Learning state with step 0)."""
    query = """
        SELECT c.id, c.word, c.meaning, c.example, c.unit, c.category
        FROM cards c
        WHERE c.category = ? AND c.participates = 1
    """
    params = [args.category or "english"]

    # Issue #4: new-word selection obeys the textbook scope exactly like
    # cmd_due (US-23: 自动安排的内容排除尚未学过的课次).
    if has_textbook_tables(db):
        scope_sql, scope_params = scope_predicate(db)
        query += scope_sql
        params.extend(scope_params)

    if args.unit:
        query += " AND c.unit = ?"
        params.append(args.unit)

    rows = db.execute(query, params).fetchall()

    new_cards = []
    for row in rows:
        # Check if card has been reviewed
        log_count = db.execute(
            "SELECT COUNT(*) FROM review_logs WHERE card_id = ?", (row["id"],)
        ).fetchone()[0]
        if log_count == 0:
            new_cards.append({
                "id": row["id"],
                "word": row["word"],
                "meaning": row["meaning"],
                "example": row["example"],
                "unit": row["unit"],
            })

    if args.limit is not None:
        new_cards = new_cards[: args.limit]

    print(json.dumps(new_cards, ensure_ascii=False, indent=2))


def cmd_review(db, args):
    """Record review results and update FSRS state."""
    raw = None
    if getattr(args, "file", None):
        try:
            with open(args.file, "r", encoding="utf-8") as f:
                raw = f.read()
        except OSError as e:
            print(f"Error: cannot read --file {args.file}: {e}")
            sys.exit(1)
    elif args.results:
        raw = args.results
    else:
        print("Error: provide --results or --file")
        sys.exit(1)

    try:
        results = json.loads(raw)
    except json.JSONDecodeError as e:
        # Fix: report malformed review JSON cleanly instead of crashing with a traceback.
        print(f"Error: invalid review JSON: {e}")
        sys.exit(1)

    # Fix: reject non-object JSON so review data cannot crash when iterating .items().
    if not isinstance(results, dict):
        print("Error: --results must be a JSON object")
        sys.exit(1)

    # results format: {"word_or_id": "good"|"hard"|"again", ...}
    rating_map = {
        "good": Rating.Good,
        "hard": Rating.Hard,
        "again": Rating.Again,
        "easy": Rating.Easy,
    }

    now = datetime.now(timezone.utc)
    updated = []

    for key, rating_str in results.items():
        # Fix: tolerate bad rating values in a batch without aborting valid cards.
        if not isinstance(rating_str, str):
            print(f"Warning: rating for '{key}' must be a string, skipping")
            continue
        rating_str = rating_str.lower().strip()
        if rating_str not in rating_map:
            print(f"Warning: unknown rating '{rating_str}' for '{key}', skipping")
            continue

        rating = rating_map[rating_str]

        # Find card by id or word
        category_filter = args.category if args.category else None
        try:
            card_id = int(key)
            row = db.execute(
                "SELECT id, word, fsrs_card FROM cards WHERE id = ?",
                (card_id,)
            ).fetchone()
        except ValueError:
            if category_filter:
                row = db.execute(
                    "SELECT id, word, fsrs_card FROM cards WHERE word = ? COLLATE NOCASE AND category = ?",
                    (key, category_filter),
                ).fetchone()
            else:
                # Fix: no --category should keep English review examples safe; prefer english,
                # then fall back only when a single non-English match exists for compatibility.
                row = db.execute(
                    "SELECT id, word, fsrs_card FROM cards WHERE word = ? COLLATE NOCASE AND category = ?",
                    (key, "english"),
                ).fetchone()
                if not row:
                    matches = db.execute(
                        "SELECT id, word, category, fsrs_card FROM cards WHERE word = ? COLLATE NOCASE",
                        (key,),
                    ).fetchall()
                    if len(matches) == 1:
                        row = matches[0]
                    elif len(matches) > 1:
                        categories = ", ".join(sorted({m["category"] for m in matches}))
                        print(
                            f"Warning: card '{key}' exists in multiple categories ({categories}); "
                            "pass --category to review it, skipping"
                        )
                        continue

        if not row:
            print(f"Warning: card '{key}' not found, skipping")
            continue

        fsrs_card = Card.from_json(row["fsrs_card"])
        fsrs_card, review_log = SCHEDULER.review_card(fsrs_card, rating, now)

        # Save updated FSRS state
        db.execute(
            "UPDATE cards SET fsrs_card = ? WHERE id = ?",
            (fsrs_card.to_json(), row["id"]),
        )

        # Save review log
        db.execute(
            "INSERT INTO review_logs (card_id, rating, reviewed_at) VALUES (?, ?, ?)",
            (row["id"], int(rating), now.isoformat()),
        )

        next_due = fsrs_card.due.astimezone(CST).strftime("%Y-%m-%d %H:%M")
        updated.append({
            "word": row["word"],
            "rating": rating_str,
            "next_due": next_due,
            "state": fsrs_card.state.name,
            "stability": round(fsrs_card.stability, 2),
        })

    db.commit()
    print(json.dumps({"updated": updated}, ensure_ascii=False, indent=2))


def cmd_stats(db, args):
    """Show vocabulary statistics."""
    category = args.category or "english"

    total = db.execute(
        "SELECT COUNT(*) FROM cards WHERE category = ?", (category,)
    ).fetchone()[0]
    total_reviews = db.execute(
        "SELECT COUNT(*) FROM review_logs WHERE card_id IN (SELECT id FROM cards WHERE category = ?)",
        (category,),
    ).fetchone()[0]

    now = datetime.now(timezone.utc)
    # Issue #4: stats describes the practice-eligible pool (same 口径 as
    # #2's participates-only stats), so the textbook scope applies to both
    # participating queries below.
    scope_sql, scope_params = ("", [])
    if has_textbook_tables(db):
        scope_sql, scope_params = scope_predicate(db)
    rows = db.execute(
        "SELECT id, fsrs_card, fetch_locked_until FROM cards c "
        "WHERE c.category = ? AND c.participates = 1" + scope_sql,
        [category] + scope_params,
    ).fetchall()

    due_count = 0
    new_count = 0
    learning_count = 0
    review_count = 0
    mastered_count = 0
    fetch_locked_count = 0  # total locked (any due status)
    due_locked_count = 0    # locked AND due (same scope as due_count)

    # fetch_locked_until rides the SAME scan (one pass over the scoped
    # pool — the scope predicate's EXISTS subqueries run once per card).
    lock_map = {r["id"]: r["fetch_locked_until"] for r in rows}
    now_str = now.strftime("%Y-%m-%d %H:%M:%S")

    for row in rows:
        fsrs_card = Card.from_json(row["fsrs_card"])
        is_due = fsrs_card.due <= now
        # Fix: stats now matches cmd_due's due-date rule, including due Learning/Relearning cards.
        if is_due:
            due_count += 1

        # Check fetch lock status
        locked_until = lock_map.get(row["id"])
        is_locked = locked_until is not None and locked_until > now_str
        if is_locked:
            fetch_locked_count += 1
            if is_due:
                due_locked_count += 1

        if fsrs_card.state == State.Learning:
            log_count = db.execute(
                "SELECT COUNT(*) FROM review_logs WHERE card_id = ?", (row["id"],)
            ).fetchone()[0]
            if log_count == 0:
                new_count += 1
            else:
                learning_count += 1
        elif fsrs_card.state == State.Relearning:
            learning_count += 1
        elif fsrs_card.state == State.Review:
            review_count += 1
            if not is_due:
                mastered_count += 1

    # Unit breakdown
    units = db.execute(
        "SELECT unit, COUNT(*) as cnt FROM cards WHERE category = ? GROUP BY unit ORDER BY unit",
        (category,),
    ).fetchall()

    unit_list = [{"unit": u["unit"] or "(未分组)", "count": u["cnt"]} for u in units]

    print(json.dumps({
        "total": total,
        "new": new_count,
        "learning": learning_count,
        "due_for_review": due_count,
        "due_available": due_count - due_locked_count if due_count > due_locked_count else 0,
        "fetch_locked": fetch_locked_count,
        "due_locked": due_locked_count,
        "mastered": mastered_count,
        "total_reviews": total_reviews,
        "units": unit_list,
    }, ensure_ascii=False, indent=2))


def cmd_list(db, args):
    """List all words."""
    category = args.category or "english"
    query = "SELECT id, word, meaning, example, unit, participates FROM cards WHERE category = ?"
    params = [category]

    if args.unit:
        query += " AND unit = ?"
        params.append(args.unit)

    rows = db.execute(query, params).fetchall()
    words = [
        {"id": r["id"], "word": r["word"], "meaning": r["meaning"], "example": r["example"], "unit": r["unit"], "participates": bool(r["participates"])}
        for r in rows
    ]
    print(json.dumps(words, ensure_ascii=False, indent=2))


def cmd_export(db, args):
    """Export cards in one category with their FSRS state."""
    category = args.category or "english"
    rows = db.execute(
        "SELECT id, word, meaning, example, unit, category, fsrs_card, created_at, fetch_locked_until, participates FROM cards WHERE category = ?",
        (category,),
    ).fetchall()

    export = []
    for row in rows:
        fsrs_card = Card.from_json(row["fsrs_card"])
        log_count = db.execute(
            "SELECT COUNT(*) FROM review_logs WHERE card_id = ?", (row["id"],)
        ).fetchone()[0]
        export.append({
            "id": row["id"],
            "word": row["word"],
            "meaning": row["meaning"],
            "example": row["example"],
            "unit": row["unit"],
            "reviews": log_count,
            "state": fsrs_card.state.name,
            "stability": round(fsrs_card.stability, 2) if fsrs_card.stability else 0,
            "difficulty": round(fsrs_card.difficulty, 2) if fsrs_card.difficulty else 0,
            "due": fsrs_card.due.astimezone(CST).strftime("%Y-%m-%d %H:%M"),
            "fetch_locked_until": row["fetch_locked_until"],
            "participates": bool(row["participates"]),
        })

    print(json.dumps(export, ensure_ascii=False, indent=2))


def cmd_reset(db, args):
    """Reset a card's FSRS state (for re-learning)."""
    card_id = args.card_id
    row = db.execute("SELECT id FROM cards WHERE id = ?", (card_id,)).fetchone()
    if not row:
        print(f"Card {card_id} not found")
        sys.exit(1)

    fsrs_card = Card()
    db.execute("UPDATE cards SET fsrs_card = ? WHERE id = ?", (fsrs_card.to_json(), card_id))
    db.commit()
    print(json.dumps({"reset": card_id}))


def cmd_edit(db, args):
    """Edit a card's text fields WITHOUT touching FSRS state (D-07).

    Absent flags keep the current value (argparse default None -> COALESCE
    keeps the old column); an explicit empty string clears the column
    (None-vs-empty distinction). The card's learning-state column is never
    referenced here — editing text must never reset learning progress (VOC-01
    edit intent; delete+re-add would wipe it, which is why this subcommand
    exists).
    """
    card_id = args.id
    row = db.execute("SELECT id FROM cards WHERE id = ?", (card_id,)).fetchone()
    if not row:
        print(f"Card {card_id} not found")
        sys.exit(1)

    db.execute(
        """
        UPDATE cards
        SET word = COALESCE(?, word),
            meaning = COALESCE(?, meaning),
            example = COALESCE(?, example),
            unit = COALESCE(?, unit)
        WHERE id = ?
        """,
        (args.word, args.meaning, args.example, args.unit, card_id),
    )
    db.commit()

    updated = db.execute("SELECT word FROM cards WHERE id = ?", (card_id,)).fetchone()
    print(json.dumps({"edited": {"id": card_id, "word": updated["word"]}}, ensure_ascii=False))


def cmd_set_participation(db, args):
    """Set classroom eligibility for a batch without changing learning data.

    All-or-nothing: the existence check and the update run inside ONE
    BEGIN IMMEDIATE transaction (same pattern as cmd_due), and any missing
    id rolls the whole batch back — the caller never sees "updated" with a
    partially applied set. The rollback diagnostic keeps the "not found"
    wording so the Go handler's stdout not-found mapping (Pitfall 2)
    surfaces it as an honest 404 instead of a partial success.
    """
    ids = _parse_card_ids(args.ids)
    db.execute("BEGIN IMMEDIATE")
    existing = {row["id"] for row in _require_cards(db, ids)}
    placeholders = ",".join("?" for _ in ids)
    value = 1 if args.participates == "true" else 0
    db.execute(
        f"UPDATE cards SET participates = ? WHERE id IN ({placeholders})",
        [value] + ids,
    )
    db.commit()
    print(json.dumps({
        "requested": len(ids), "updated": len(existing), "missing": [],
    }, ensure_ascii=False))


def _validate_lessons(raw):
    """Parse/validate --lessons: a JSON array of lesson names in EXPLICIT
    teaching order (Issue #4: never inferred from dictionary order). Empty
    is legal (a textbook may be created name-only and given its lesson
    list later, US-8). Returns the cleaned list."""
    try:
        lessons = json.loads(raw)
    except (TypeError, ValueError):
        print("Error: --lessons must be a JSON array of lesson names")
        sys.exit(1)
    if not isinstance(lessons, list) or len(lessons) > 200:
        print("Error: --lessons must contain at most 200 lesson names")
        sys.exit(1)
    cleaned = []
    for i, name in enumerate(lessons):
        if not isinstance(name, str):
            print(f"Error: lesson {i} must be a string, got: {type(name).__name__}")
            sys.exit(1)
        name = name.strip()
        if not name or len(name) > 100:
            print(f"Error: lesson {i} must be 1..100 non-space characters")
            sys.exit(1)
        cleaned.append(name)
    if len(set(cleaned)) != len(cleaned):
        print("Error: lesson names must be unique within a textbook")
        sys.exit(1)
    return cleaned


def _parse_card_ids(raw):
    """Parse/validate --ids: 1..500 unique positive integers (same shape as
    cmd_set_participation — the batch cap the Go layer also enforces)."""
    try:
        ids = json.loads(raw)
    except (TypeError, ValueError):
        print("Error: --ids must be a JSON array of integers")
        sys.exit(1)
    if (not isinstance(ids, list) or not 1 <= len(ids) <= 500 or
            not all(isinstance(i, int) and not isinstance(i, bool) and i > 0 for i in ids) or
            len(set(ids)) != len(ids)):
        print("Error: --ids must contain 1..500 unique positive integers")
        sys.exit(1)
    return ids


def _require_cards(db, ids, cols="id"):
    """Resolve a batch's card rows inside an OPEN BEGIN IMMEDIATE
    transaction; a missing id rolls back with the shared not-found
    diagnostic (the wording the Go handler maps to 404, Pitfall 2)."""
    placeholders = ",".join("?" for _ in ids)
    rows = db.execute(
        f"SELECT {cols} FROM cards WHERE id IN ({placeholders})", ids
    ).fetchall()
    missing = [i for i in ids if i not in {r["id"] for r in rows}]
    if missing:
        db.rollback()
        print("Error: card ids not found: " + ", ".join(str(i) for i in missing))
        sys.exit(1)
    return rows


def cmd_textbook_create(db, args):
    """Create a textbook. Only the name is required; edition/stage/grade/
    volume/module are free-form descriptions and the lesson list may be
    filled in later via textbook-update (US-7/US-8: 不要求填满所有层级)."""
    name = args.name.strip()
    if not name or len(name) > 100:
        print("Error: --name must be 1..100 non-space characters")
        sys.exit(1)
    if args.category not in ("english", "chinese"):
        print("Error: --category must be english or chinese")
        sys.exit(1)
    lessons = _validate_lessons(args.lessons) if args.lessons else []
    try:
        cur = db.execute(
            """INSERT INTO textbooks
               (name, category, edition, stage, grade, volume, module, lessons, learned_lessons)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)""",
            (name, args.category, args.edition.strip(), args.stage.strip(),
             args.grade.strip(), args.volume.strip(), args.module.strip(),
             json.dumps(lessons, ensure_ascii=False)),
        )
        db.commit()
    except sqlite3.IntegrityError:
        db.rollback()
        print(f"Error: textbook name already exists in category '{args.category}': {name}")
        sys.exit(1)
    print(json.dumps({"id": cur.lastrowid, "name": name}, ensure_ascii=False))


def cmd_textbook_update(db, args):
    """Update a textbook: any subset of name/metadata/lesson list/progress/
    primary in one transaction. --learned-lessons is validated against the
    FINAL lesson list (the new one when --lessons is also given). Setting
    --set-primary demotes every other primary in the same category (主教材
    per category is unique). Renaming lessons does NOT rewrite assignments:
    an assignment naming a lesson that left the list keeps its name but no
    longer counts as learned (课表外), the same honest rule as 待确认."""
    row = db.execute("SELECT * FROM textbooks WHERE id = ?", (args.id,)).fetchone()
    if row is None:
        print(f"Error: textbook id not found: {args.id}")
        sys.exit(1)

    sets, params = [], []
    if args.name is not None:
        name = args.name.strip()
        if not name or len(name) > 100:
            print("Error: --name must be 1..100 non-space characters")
            sys.exit(1)
        sets.append("name = ?")
        params.append(name)
    for field in ("edition", "stage", "grade", "volume", "module"):
        value = getattr(args, field)
        if value is not None:
            value = value.strip()
            if len(value) > 100:
                print(f"Error: --{field} must be at most 100 characters")
                sys.exit(1)
            sets.append(f"{field} = ?")
            params.append(value)
    lessons = None
    if args.lessons is not None:
        lessons = _validate_lessons(args.lessons)
        sets.append("lessons = ?")
        params.append(json.dumps(lessons, ensure_ascii=False))
    if args.learned_lessons is not None:
        try:
            current = json.loads(row["lessons"])
        except (TypeError, ValueError):
            current = []
        total = len(lessons) if lessons is not None else len(current)
        if not 0 <= args.learned_lessons <= total:
            print(f"Error: --learned-lessons must be 0..{total} for this textbook's lesson list")
            sys.exit(1)
        sets.append("learned_lessons = ?")
        params.append(args.learned_lessons)

    db.execute("BEGIN IMMEDIATE")
    if args.set_primary:
        db.execute(
            "UPDATE textbooks SET is_primary = 0 WHERE category = ? AND id != ?",
            (row["category"], args.id),
        )
        sets.append("is_primary = 1")
    if sets:
        params.append(args.id)
        try:
            db.execute(f"UPDATE textbooks SET {', '.join(sets)} WHERE id = ?", params)
        except sqlite3.IntegrityError:
            db.rollback()
            print(f"Error: textbook name already exists in category '{row['category']}'")
            sys.exit(1)
    db.commit()
    print(json.dumps({"id": args.id, "updated": True}, ensure_ascii=False))


def cmd_assign_words(db, args):
    """Batch-assign cards to one textbook's lesson (or 待确认 when --lesson
    is empty). All-or-nothing with the #2 transaction shape: a missing card
    id rolls the whole batch back with a not-found diagnostic; the lesson
    must be '' or belong to the textbook's lesson list; every card's
    category must match the textbook's. Re-assigning replaces the previous
    lesson/include choice for that (card, textbook)."""
    ids = _parse_card_ids(args.ids)
    textbook = db.execute(
        "SELECT id, name, category, lessons FROM textbooks WHERE id = ?",
        (args.textbook_id,),
    ).fetchone()
    if textbook is None:
        print(f"Error: textbook id not found: {args.textbook_id}")
        sys.exit(1)
    lesson = args.lesson or ""
    try:
        lessons = json.loads(textbook["lessons"])
    except (TypeError, ValueError):
        lessons = []
    if lesson and lesson not in lessons:
        print(f"Error: lesson {lesson!r} is not in textbook {textbook['name']!r}'s lesson list")
        sys.exit(1)
    include = 1 if args.include_in_scope else 0

    db.execute("BEGIN IMMEDIATE")
    rows = _require_cards(db, ids, cols="id, category")
    mismatched = [str(r["id"]) for r in rows if r["category"] != textbook["category"]]
    if mismatched:
        db.rollback()
        print(
            f"Error: card category does not match textbook category "
            f"'{textbook['category']}': " + ", ".join(mismatched)
        )
        sys.exit(1)
    for card_id in ids:
        db.execute(
            """INSERT INTO card_assignments (card_id, textbook_id, lesson, include_in_scope)
               VALUES (?, ?, ?, ?)
               ON CONFLICT(card_id, textbook_id)
               DO UPDATE SET lesson = excluded.lesson, include_in_scope = excluded.include_in_scope""",
            (card_id, args.textbook_id, lesson, include),
        )
    db.commit()
    print(json.dumps({
        "requested": len(ids), "updated": len(ids), "missing": [],
        "textbook_id": args.textbook_id, "lesson": lesson,
        "include_in_scope": bool(include),
    }, ensure_ascii=False))


def cmd_unassign_words(db, args):
    """Remove cards' assignments to one textbook (the word returns to
    supplementary vocabulary: eligible by default, participation switch
    alone governs it). Missing ids still roll the batch back for
    symmetry with assign-words."""
    ids = _parse_card_ids(args.ids)
    textbook = db.execute(
        "SELECT id FROM textbooks WHERE id = ?", (args.textbook_id,)
    ).fetchone()
    if textbook is None:
        print(f"Error: textbook id not found: {args.textbook_id}")
        sys.exit(1)
    db.execute("BEGIN IMMEDIATE")
    placeholders = ",".join("?" for _ in ids)
    _require_cards(db, ids)
    cur = db.execute(
        f"DELETE FROM card_assignments WHERE textbook_id = ? AND card_id IN ({placeholders})",
        [args.textbook_id] + ids,
    )
    db.commit()
    print(json.dumps({
        "requested": len(ids), "removed": cur.rowcount, "missing": [],
        "textbook_id": args.textbook_id,
    }, ensure_ascii=False))


def cmd_rename_unit(db, args):
    """Rename a unit across all cards in a category."""
    old_name = args.old_unit.strip()
    new_name = args.new_unit.strip()
    if not old_name or not new_name:
        print("Error: --old-unit and --new-unit must be non-empty strings")
        sys.exit(1)

    cur = db.execute(
        "SELECT COUNT(*) FROM cards WHERE category = ? AND unit = ?",
        (args.category, old_name),
    )
    count = cur.fetchone()[0]
    if count == 0:
        print(f"No cards found with unit '{old_name}' in category '{args.category}'")
        sys.exit(1)

    db.execute(
        "UPDATE cards SET unit = ? WHERE category = ? AND unit = ?",
        (new_name, args.category, old_name),
    )
    db.commit()
    print(json.dumps({"renamed": count, "from": old_name, "to": new_name}))


def cmd_delete_words(db, args):
    """Delete words by unit or by explicit word list. Requires --confirm for safety."""
    if not args.unit and not args.words:
        print("Error: provide --unit or --words to specify what to delete")
        sys.exit(1)

    if args.unit:
        # --- unit mode ---
        rows = db.execute(
            "SELECT id, word, unit FROM cards WHERE category = ? AND unit = ?",
            (args.category, args.unit),
        ).fetchall()
        if not rows:
            # Not-found is an idempotent no-op: print a trailing JSON result
            # (not the old plain-text diagnostic) so the Go bridge's
            # classifyMgmtRun/extractTrailingJSON parses a typed result
            # instead of erroring ErrPythonJSONParse → 502 (WR-03).
            print(json.dumps({"deleted": 0}))
            sys.exit(0)

        card_ids = [r["id"] for r in rows]

        if not args.confirm:
            print(f"Would delete {len(rows)} card(s). Add --confirm to proceed:")
            for r in rows:
                print(f"  [{r['id']}] {r['word']} (unit: {r['unit'] or '(none)'})")
            sys.exit(0)

        # Delete review_logs and textbook assignments first, then cards
        placeholders = ",".join("?" for _ in card_ids)
        db.execute(f"DELETE FROM review_logs WHERE card_id IN ({placeholders})", card_ids)
        db.execute(f"DELETE FROM card_assignments WHERE card_id IN ({placeholders})", card_ids)
        cur = db.execute(
            "DELETE FROM cards WHERE category = ? AND unit = ?",
            (args.category, args.unit),
        )
        db.commit()
        print(json.dumps({"deleted": cur.rowcount, "unit": args.unit}))
    else:
        # --- words mode ---
        word_list = [w.strip() for w in args.words.split(",") if w.strip()]
        if not word_list:
            print("Error: --words list is empty after parsing")
            sys.exit(1)

        placeholders = ",".join("?" for _ in word_list)
        rows = db.execute(
            f"SELECT id, word, unit FROM cards WHERE category = ? AND word IN ({placeholders})",
            [args.category] + word_list,
        ).fetchall()
        if not rows:
            # Words-mode not-found: same idempotent-JSON contract as unit
            # mode above (WR-03).
            print(json.dumps({"deleted": 0}))
            sys.exit(0)

        card_ids = [r["id"] for r in rows]

        if not args.confirm:
            print(f"Would delete {len(rows)} card(s). Add --confirm to proceed:")
            for r in rows:
                print(f"  [{r['id']}] {r['word']} (unit: {r['unit'] or '(none)'})")
            sys.exit(0)

        # Delete review_logs and textbook assignments first, then cards
        id_placeholders = ",".join("?" for _ in card_ids)
        db.execute(f"DELETE FROM review_logs WHERE card_id IN ({id_placeholders})", card_ids)
        db.execute(f"DELETE FROM card_assignments WHERE card_id IN ({id_placeholders})", card_ids)
        cur = db.execute(
            f"DELETE FROM cards WHERE category = ? AND word IN ({placeholders})",
            [args.category] + word_list,
        )
        db.commit()
        print(json.dumps({"deleted": cur.rowcount, "words": word_list}))


def cmd_merge_units(db, args):
    """Merge one or more source units into a target unit."""
    target = args.target.strip()
    sources = [s.strip() for s in args.sources.split(",") if s.strip()]
    if not target:
        print("Error: --target must be a non-empty string")
        sys.exit(1)
    if not sources:
        print("Error: --sources must be a comma-separated list of unit names")
        sys.exit(1)
    if target in sources:
        print("Error: --target cannot be one of the --sources")
        sys.exit(1)

    total_moved = 0
    for src in sources:
        cur = db.execute(
            "UPDATE cards SET unit = ? WHERE category = ? AND unit = ?",
            (target, args.category, src),
        )
        total_moved += cur.rowcount

    db.commit()
    print(json.dumps({"moved": total_moved, "from": sources, "to": target, "category": args.category}))


def cmd_init(db, args):
    """Issue #78: guarantee the Python-owned schema exists (no data changes).

    main() already runs init_db(db) unconditionally before dispatch, so the
    schema is present by the time this runs. The command exists purely as an
    explicit, side-effect-free entrypoint for installers / first-boot to invoke,
    instead of leaning on a data command (e.g. `list`) whose output and side
    effects they do not want.
    """
    # Schema already ensured by main(); nothing further to do.


def main():
    parser = argparse.ArgumentParser(description="Vocabulary Manager with FSRS")
    parser.add_argument("--db", default=DB_PATH, help="Database path")
    subparsers = parser.add_subparsers(dest="command", help="Command")

    # init (Issue #78): explicit, side-effect-free schema bootstrap. Fresh
    # installs reach /api/v1/units (Go reads SQLite directly, no Python
    # process) before any vocab command has ever run, so cards/textbooks/
    # review_logs never get created -> "no such table: cards". Installers call
    # this once (idempotent) to guarantee the Python-owned schema exists.
    p_init = subparsers.add_parser(
        "init",
        help="Create the database schema if missing (idempotent, no data changes)",
        description="Create the Python-owned schema (cards/textbooks/review_logs/card_assignments) if missing. Idempotent.",
    )

    # add
    p_add = subparsers.add_parser("add", help="Add words")
    p_add.add_argument("--unit", default="", help="Unit/lesson name")
    p_add.add_argument("--category", default="english", help="Category (default: english)")
    p_add.add_argument("--words", help='JSON string: {"word":"meaning",...} or [{"word":...},...]')
    p_add.add_argument("--file", help="JSON file with words")

    # due
    p_due = subparsers.add_parser("due", help="Get due cards")
    p_due.add_argument("--unit", default="", help="Filter by unit (exact match)")
    p_due.add_argument("--units", default="", help="Filter by multiple units (comma-separated, e.g. 'L10纸的发明,L11赵州桥')")
    p_due.add_argument("--category", default="english", help="Category")
    p_due.add_argument("--limit", type=bounded_positive_int(MAX_CARD_LIMIT), help="Max cards to return")
    p_due.add_argument("--exclude", default="", help="Exclude words already read out (comma-separated)")
    p_due.add_argument("--peek", action="store_true", help="Preview due cards without marking them as fetched (no side effects)")
    p_due.add_argument("--fetch-lock-hours", type=bounded_positive_int(MAX_FETCH_LOCK_HOURS), default=None, help=f"Hours to lock fetched cards (default: {FETCH_LOCK_HOURS})")

    # new
    p_new = subparsers.add_parser("new", help="Get new (unreviewed) cards")
    p_new.add_argument("--unit", default="", help="Filter by unit")
    p_new.add_argument("--category", default="english", help="Category")
    p_new.add_argument("--limit", type=bounded_positive_int(MAX_CARD_LIMIT), help="Max cards to return")

    # review
    p_review = subparsers.add_parser("review", help="Record review results")
    p_review.add_argument("--results", help='JSON: {"word_or_id":"good|hard|again",...}')
    p_review.add_argument("--file", help="Path to JSON file with results (avoids long CLI args / approval prompts)")
    p_review.add_argument("--category", default="", help="Category for word lookup (defaults to english; use chinese for Chinese dictation)")

    # stats
    p_stats = subparsers.add_parser("stats", help="Show statistics")
    p_stats.add_argument("--category", default="english", help="Category")

    # list
    p_list = subparsers.add_parser("list", help="List all words")
    p_list.add_argument("--unit", default="", help="Filter by unit")
    p_list.add_argument("--category", default="english", help="Category")

    # export
    # Fix: export is intentionally category-scoped, so help text should not promise all cards.
    p_export = subparsers.add_parser(
        "export",
        help="Export cards in a category",
        description="Export cards in a category",
    )
    p_export.add_argument("--category", default="english", help="Category to export (default: english)")

    # reset
    p_reset = subparsers.add_parser("reset", help="Reset a card's FSRS state")
    p_reset.add_argument("card_id", type=int, help="Card ID to reset")

    # rename-unit
    p_rename = subparsers.add_parser("rename-unit", help="Rename a unit")
    p_rename.add_argument("--old-unit", required=True, help="Current unit name")
    p_rename.add_argument("--new-unit", required=True, help="New unit name")
    p_rename.add_argument("--category", default="english", help="Category (default: english)")

    # edit
    p_edit = subparsers.add_parser("edit", help="Edit a card's text fields without touching FSRS state")
    p_edit.add_argument("--id", type=int, required=True, help="Card ID to edit")
    p_edit.add_argument("--word", default=None, help="New word text (absent keeps current; empty string clears)")
    p_edit.add_argument("--meaning", default=None, help="New meaning (absent keeps current; empty string clears)")
    p_edit.add_argument("--example", default=None, help="New example (absent keeps current; empty string clears)")
    p_edit.add_argument("--unit", default=None, help="New unit (absent keeps current; empty string clears)")

    p_participation = subparsers.add_parser("set-participation", help="Set classroom participation for cards")
    p_participation.add_argument("--ids", required=True, help="JSON array of card IDs")
    p_participation.add_argument("--participates", choices=("true", "false"), required=True)

    # textbook-create / textbook-update / assign-words / unassign-words
    # (Issue #4: textbook attribution + learned progress)
    p_tb_create = subparsers.add_parser("textbook-create", help="Create a textbook")
    p_tb_create.add_argument("--name", required=True, help="Textbook name (unique per category)")
    p_tb_create.add_argument("--category", default="english", help="Category (english|chinese)")
    for meta in ("edition", "stage", "grade", "volume", "module"):
        p_tb_create.add_argument(f"--{meta}", default="", help=f"Optional {meta} description")
    p_tb_create.add_argument("--lessons", default="", help="JSON array of lesson names in explicit teaching order")

    p_tb_update = subparsers.add_parser("textbook-update", help="Update textbook fields, lesson list, progress or primary flag")
    p_tb_update.add_argument("--id", type=int, required=True, help="Textbook ID")
    p_tb_update.add_argument("--name", default=None, help="New name (absent keeps current)")
    for meta in ("edition", "stage", "grade", "volume", "module"):
        p_tb_update.add_argument(f"--{meta}", default=None, help=f"New {meta} (absent keeps current)")
    p_tb_update.add_argument("--lessons", default=None, help="New JSON lesson array (absent keeps current)")
    p_tb_update.add_argument("--learned-lessons", type=int, default=None,
                             help="已学完第 N 课 (0..len(lessons); includes lesson N)")
    p_tb_update.add_argument("--set-primary", action="store_true",
                             help="Make this the category's primary textbook (demotes others)")

    p_assign = subparsers.add_parser("assign-words", help="Batch-assign cards to a textbook lesson (empty lesson = 待确认)")
    p_assign.add_argument("--textbook-id", type=int, required=True)
    p_assign.add_argument("--ids", required=True, help="JSON array of card IDs (1..500 unique)")
    p_assign.add_argument("--lesson", default="", help="Lesson name from the textbook's list; empty = 待确认")
    p_assign.add_argument("--include-in-scope", action="store_true",
                          help="Parent's explicit include-in-scope override (regardless of lesson progress)")

    p_unassign = subparsers.add_parser("unassign-words", help="Remove cards' assignments to a textbook")
    p_unassign.add_argument("--textbook-id", type=int, required=True)
    p_unassign.add_argument("--ids", required=True, help="JSON array of card IDs (1..500 unique)")

    # delete-words
    p_delete = subparsers.add_parser("delete-words", help="Delete words by unit or word list (dry-run by default)")
    delete_target = p_delete.add_mutually_exclusive_group(required=True)
    delete_target.add_argument("--unit", default="", help="Delete all words in this unit")
    delete_target.add_argument("--words", default="", help="Comma-separated word list to delete")
    p_delete.add_argument("--category", default="english", help="Category (default: english)")
    p_delete.add_argument("--confirm", action="store_true", help="Actually delete (without this flag, only previews)")

    # merge-units
    p_merge = subparsers.add_parser("merge-units", help="Merge source units into a target unit")
    p_merge.add_argument("--target", required=True, help="Target unit name")
    p_merge.add_argument("--sources", required=True, help="Comma-separated source unit names")
    p_merge.add_argument("--category", default="english", help="Category (default: english)")

    # unlock
    p_unlock = subparsers.add_parser("unlock", help="Clear fetch locks (scoped --ids release, or category/unit sweep)")
    p_unlock.add_argument("--category", default="english", help="Category (default: english; ignored with --ids)")
    p_unlock.add_argument("--unit", default="", help="Filter by unit (default: all units; ignored with --ids)")
    p_unlock.add_argument("--ids", default="", help="JSON array of card IDs to unlock (scoped release; takes precedence over category/unit)")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    db = get_db(args.db)
    init_db(db)

    commands = {
        "init": cmd_init,
        "add": cmd_add,
        "due": cmd_due,
        "new": cmd_new,
        "review": cmd_review,
        "stats": cmd_stats,
        "list": cmd_list,
        "export": cmd_export,
        "reset": cmd_reset,
        "edit": cmd_edit,
        "set-participation": cmd_set_participation,
        "textbook-create": cmd_textbook_create,
        "textbook-update": cmd_textbook_update,
        "assign-words": cmd_assign_words,
        "unassign-words": cmd_unassign_words,
        "rename-unit": cmd_rename_unit,
        "delete-words": cmd_delete_words,
        "merge-units": cmd_merge_units,
        "unlock": cmd_unlock,
    }

    try:
        commands[args.command](db, args)
    finally:
        db.close()


if __name__ == "__main__":
    main()
