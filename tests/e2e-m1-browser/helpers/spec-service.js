// spec-service.js — per-TEST isolated real service + strict-stub control for
// the m1 browser line (issue #58, e2e-test-plan §7 steps 11–13). Plan §2's
// isolation spec asks for 每用例独立数据库与数据目录, and the classroom
// specs genuinely need it: a lesson row is FIRST-CONTENT-WINS per pinned
// lesson id + subject (pkg/lesson EnsureLessonCards INSERT OR IGNORE — the
// run-level webServer instance can hand out ONE row per subject for its
// whole lifetime). Every spec below opens classrooms with ITS OWN words, so
// each test here launches its own launcher instance (own temp SQLite, own
// loopback ports — service-launcher.js), never touching the shared 18290
// instance or another suite's ports. Origin still passes: the template's
// bare "127.0.0.1" whitelist entry matches any port (pkg/rt conn.go's
// port-tolerant fallback).
//
// The strict stub (127.0.0.1:18285) stays SHARED for the whole run — the
// per-test services all point voice.baseUrl at it via the config template.
// Because smoke.spec.js (alphabetically between the new files) asserts the
// stub recorded ZERO requests, every spec using this helper must
// stubReset() when it ends: reset clears requests/violations/scripts so the
// next spec starts from a clean, strictly-scripted slate.
//
// Node test: spec-service.test.js (stub-control roundtrip against an
// in-process stub — the Go boot path is covered by service-launcher tests
// and every spec itself).
'use strict';

const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { launchService, resolvePython } = require('./service-launcher');

const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');
const TEMPLATE = path.join(__dirname, '..', 'config.m1-e2e.yaml');
const STUB_BASE = 'http://127.0.0.1:18285';

/**
 * Launch one isolated real service for a single test. Returns
 *   { url, stop, seedWords, deleteWords, listWords, listLessons }
 *   url        — http://127.0.0.1:<port>/ (absolute: these pages do NOT use
 *                the config baseURL, whose 18290 instance is not this one)
 *   stop()     — idempotent teardown (kills the child, removes its temp root)
 *   seedWords({category, unit, entries}) — real REST → Python → SQLite write
 *   deleteWords({category, words}) — the parent's real confirmed delete
 *                (DELETE /words → python delete-words --confirm; the payload
 *                carries {"deleted": N}) — the mid-session word removal the
 *                J14 partial-save leg stages through the product surface
 *   listWords({category}) / listLessons() — real read path for assertions
 */
async function startSpecService() {
  const svc = await launchService({ repoRoot: REPO_ROOT, template: TEMPLATE });

  const json = async (pathname, init) => {
    const res = await fetch(svc.url + 'api/v1' + pathname, {
      ...init,
      headers: { 'content-type': 'application/json', ...(init && init.headers) },
    });
    return { status: res.status, ok: res.ok, body: await res.json().catch(() => null) };
  };

  return {
    url: svc.url,
    stop: () => svc.stop(),
    seedWords({ category, unit, entries }) {
      return json('/words', { method: 'POST', body: JSON.stringify({ category, unit, entries }) });
    },
    deleteWords({ category, words }) {
      return json('/words', { method: 'DELETE', body: JSON.stringify({ category, words }) });
    },
    listWords({ category }) {
      return json('/words?category=' + encodeURIComponent(category));
    },
    listLessons() {
      return json('/lessons');
    },
    // The in-progress REST projection intentionally hides answers. Read
    // ONLY this launcher's isolated database, in SQLite read-only mode.
    readLessonCards(lessonId) {
      const script = [
        'import json, pathlib, sqlite3, sys',
        'uri = pathlib.Path(sys.argv[1]).resolve().as_uri() + "?mode=ro"',
        'with sqlite3.connect(uri, uri=True) as db:',
        ' row = db.execute("SELECT cards FROM lessons WHERE lesson_id = ?", (sys.argv[2],)).fetchone()',
        ' if row is None: raise RuntimeError("lesson not persisted")',
        ' print(row[0])',
      ].join('\n');
      return JSON.parse(execFileSync(resolvePython(REPO_ROOT),
        ['-c', script, path.join(svc.root, 'vocab.db'), lessonId],
        { encoding: 'utf8', timeout: 5000 }));
    },
  };
}

/** Stub control bound to a base URL (the shared STUB by default). */
function makeStubControl(base = STUB_BASE) {
  const call = (pathname, init) => fetch(base + pathname, init).then((r) => r.json());
  return {
    /** Install per-category FIFO scripts (replaces the previous arrays). */
    scripts(def) {
      return call('/__strict/scripts', {
        method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(def),
      });
    },
    /** {violations, requests, remaining} — assertion view. */
    state() {
      return call('/__strict/state');
    },
    /** Clear requests/violations/scripts — required before the next spec. */
    reset() {
      return call('/__strict/reset', { method: 'POST' });
    },
  };
}

module.exports = { startSpecService, makeStubControl, STUB_BASE };
