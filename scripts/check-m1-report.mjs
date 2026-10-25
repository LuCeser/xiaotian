#!/usr/bin/env node
/**
 * 必测清单核验 for the milestone-1 browser line (issue #58, e2e-test-plan
 * §6 rule 8 / §7 step 16): a green Playwright exit code is NOT enough —
 * the plan forbids the case where the browser, Python or the launcher goes
 * missing and the run quietly reports nothing (「不能因 Python、浏览器或
 * 启动器缺失而全部 skip 后通过」).
 *
 * Reads the Playwright JSON report (PLAYWRIGHT_JSON_OUTPUT_NAME) and fails
 * unless the B-layer core set really executed AND really passed:
 *
 *   - IDENTITY: every REQUIRED_CASES entry must appear in the report by
 *     file + test title. The old file-level floor ("≥ 1 test per spec,
 *     ≥ 7 overall") stayed green when a required finish case was deleted
 *     and a filler test landed in smoke (review @0d2d125 P2-1). Titles are
 *     the case identity: renaming a test() in a spec file must update this
 *     list too — that canary is the point.
 *   - REAL PASS: "passed" means result status 'passed' with expectedStatus
 *     'passed'. stats.expected counts a test.fail() that failed as
 *     expected exactly like a normal pass (review @0d2d125 P2-2), so the
 *     stats counters are never trusted for success: every test row in the
 *     report — required or not — must be a real pass; skips, expected
 *     failures, no-result rows and flaky stats all fail the check.
 *
 * A new spec appends its mandatory cases here. Plan step 27's acceptance
 * manifest will replace this whole check.
 */
import fs from 'node:fs';
import { pathToFileURL } from 'node:url';

export const REQUIRED_CASES = [
  ['smoke.spec.js', 'real pages and REST use one fresh isolated service without vendor traffic'],
  ['subject-playback.spec.js', 'J05 mid-ask button pick: real epoch switch, no turn_start, natural new-opening ack'],
  ['voice-input.spec.js', 'synthetic mic rides the production capture path: format, silence, isolation, release'],
  ['finish.spec.js', 'J15 active end stops the classroom: ask-park, mid-answer, mid-greeting, listening'],
  ['finish.spec.js', 'J13+J14 last word → one check entry, masked answers, real check submit, natural end'],
  ['finish.spec.js', 'J16 finish chinese, reload → the english classroom opens on its own words, both submit independently'],
  ['finish.spec.js', 'J16 empty vocabulary ends through the real no-due result'],
];

// Flatten the report's suite tree. Specs carry their own file; specs under
// nested describes inherit the enclosing suite's.
function walkedSpecs(report) {
  const specs = [];
  const walk = (suite, inheritedFile) => {
    const file = suite.file || inheritedFile;
    for (const spec of suite.specs || []) {
      specs.push({ file: spec.file || file, title: spec.title, tests: spec.tests || [] });
    }
    for (const child of suite.suites || []) walk(child, file);
  };
  for (const suite of report.suites || []) walk(suite, undefined);
  return specs;
}

const label = ([file, title]) => `${file} › ${title}`;

const lastStatus = (t) => {
  const last = t.results && t.results.length ? t.results[t.results.length - 1] : null;
  return last ? last.status : 'no result';
};

export function checkM1Report(report) {
  const problems = [];
  const specs = walkedSpecs(report);

  for (const id of REQUIRED_CASES) {
    const found = specs.filter((s) => s.file && s.file.endsWith(id[0]) && s.title === id[1]);
    if (found.length === 0) {
      problems.push(`required case missing from the report: ${label(id)} — deleted, renamed or never ran`);
    } else if (found.every((s) => s.tests.length === 0)) {
      problems.push(`required case produced no test rows: ${label(id)}`);
    }
  }

  for (const s of specs) {
    for (const t of s.tests) {
      const status = lastStatus(t);
      if (t.expectedStatus !== 'passed') {
        problems.push(`not a real pass: ${s.file} › ${s.title} — expectedStatus '${t.expectedStatus}' (test.fail/fixme annotation), last result '${status}'`);
      } else if (status !== 'passed') {
        problems.push(`did not pass: ${s.file} › ${s.title} — last result '${status}'`);
      }
    }
  }

  // A flaky retry ends on a passed row, so the sweep above cannot see it;
  // only the stats counter can.
  if (report.stats && report.stats.flaky) {
    problems.push(`${report.stats.flaky} flaky test(s) — a retry-pass is not a stable pass`);
  }
  return problems;
}

// CLI entry (the CI step); tests import checkM1Report directly instead.
let isCli = false;
try {
  isCli = import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href;
} catch {
  // argv[1] missing or pointing elsewhere — running as an imported module.
}
if (isCli) {
  const reportPath = process.argv[2];
  if (!reportPath) {
    console.error('usage: node scripts/check-m1-report.mjs <playwright-json-report>');
    process.exit(2);
  }
  let report;
  try {
    report = JSON.parse(fs.readFileSync(reportPath, 'utf8'));
  } catch (err) {
    console.error(`[m1-report-check] FAIL: cannot read report ${reportPath}: ${err.message}`);
    process.exit(1);
  }
  const problems = checkM1Report(report);
  if (problems.length) {
    console.error('[m1-report-check] FAIL:\n  - ' + problems.join('\n  - '));
    process.exit(1);
  }
  console.log(`[m1-report-check] ok: all ${REQUIRED_CASES.length} required cases ran by identity and really passed (0 skip/flaky/expected-failure)`);
}
