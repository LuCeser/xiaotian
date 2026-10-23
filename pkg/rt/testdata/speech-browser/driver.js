// driver.js — Go 侧 rtbrowser 浏览器测试的 Node 驱动辅助器（issue #69
// 备选一步骤 2/3）。它只做三类事：打开生产 /tutor/ 页面、执行真实 UI 操作
// （点击页面按钮）、通过只读观察器（tests/e2e-m1-browser/helpers/rt-observe.js）
// 记录页面真实发生的 WS 帧与原生播放轨迹，并把观察结果以一行 JSON 返回给
// Go 测试。麦克风来源复用 fake-mic.js（tests/e2e-m1-browser/helpers）：
// 页面生产 capture 流程调用的 getUserMedia 被替换为合成静音流，页面无感知。
//
// 硬约束（计划步骤 3）：本驱动器绝不提供替浏览器发送 ACK、替服务端补发消息
// 的接口——没有执行任意页面 JS 的通路，不包装 WS send，不构造 WebSocket，
// scenario 是固定枚举；任何新能力只能以新增固定 scenario 函数的方式加入并在
// SCENARIOS 登记。`--check` 自检会机械验证这一点（见 checkSelf()）。
//
// CLI：
//   node driver.js --url http://127.0.0.1:P/tutor/ [--scenario fixture] [--timeout-ms 45000]
//   node driver.js --check
// 输出：stdout 末行 "SPEECH_RESULT " + JSON（Go 端解析）；退出码 0=ok。
'use strict';

const fs = require('node:fs');
const { chromium } = require('@playwright/test');
const {
  installRtObserver, waitWs, wsView, playbackState, playedBeforeAck,
} = require('../../../../tests/e2e-m1-browser/helpers/rt-observe');
const { installFakeMic, micInfo } = require('../../../../tests/e2e-m1-browser/helpers/fake-mic');

// 与 tests/playwright.m1.config.js 相同的 headless 麦克风授旗：真实页面在生产
// capture 流程里调用 getUserMedia，flags 保证 headless 下权限不弹窗；音频来源
// 由 fake-mic.js 的合成流提供。
const LAUNCH_ARGS = [
  '--use-fake-ui-for-media-stream',
  '--use-fake-device-for-media-stream',
];

function emit(result) {
  process.stdout.write('SPEECH_RESULT ' + JSON.stringify(result) + '\n');
}

// checkSelf — 辅助器自检（计划步骤 3「辅助器检查」）：机械验证驱动器自身
// 的能力面。源码级断言（对 driver.js 自己，不含 helpers——helpers 的行为由
// rt-observe.test.js / 规格用例各自背书）：
//   1. 没有执行任意页面 JS 的 evaluate 通路（含 eval 族捷径）；
//   2. 没有帧发送调用，也没有 WebSocket 构造——不替页面或服务器发送任何帧；
//   3. SCENARIOS 里登记的都是本文件定义的固定 scenario 函数。
// 敏感串拆成两段再 join，避免自检模式匹配到自身源码字面量。
function checkSelf() {
  const src = fs.readFileSync(__filename, 'utf8');
  const forbidden = [
    [['page', '.evaluate'], 'evaluate 通路（任意页面 JS）'],
    [['page', '.$eval'], 'eval 族捷径'],
    [['.', 'send('], '帧发送调用'],
    [['new ', 'WebSocket'], 'WebSocket 构造'],
  ];
  const violations = [];
  for (const [parts, why] of forbidden) {
    if (src.includes(parts.join(''))) violations.push(why);
  }
  const scenarios = Object.keys(SCENARIOS);
  for (const name of scenarios) {
    if (typeof SCENARIOS[name] !== 'function') violations.push('scenario ' + name + ' 不是函数');
  }
  return {
    ok: violations.length === 0,
    scenarios,
    micSource: 'fake-mic.js (synthetic getUserMedia, helpers 既有工具)',
    observer: 'rt-observe.js (read-only WebSocket/playback observation)',
    violations,
  };
}

// scenario "fixture"：真实页面连接 /rt，点击「开始上课」，等待页面原生播放
// 完成整段教师开场语音——每句都真实发出 sentence_ack（页面的 out 帧，只读
// 观察），且服务器在全部 ACK 后恢复了 listening 状态（in state 帧），说明这
// 是真实的自然完成，不是发送完成或超时捷径。结果里逐句报告
// playedBeforeAck（该句原生 onended 是否先于其 ACK——空播 ACK/丢块 ACK 在
// 此必为 false）与合成麦克风被页面采用的情况。
async function scenarioFixture({ page, url, timeoutMs }) {
  await page.goto(url);
  await page.locator('#rt-start').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'out' && e.type === 'sentence_ack')
    && view.some((e) => e.dir === 'in' && e.type === 'state' && e.msg && e.msg.value === 'listening'),
  { timeoutMs, label: 'natural opening playback: sentence_acks then listening restored' });

  const view = await wsView(page);
  const playback = await playbackState(page);
  const binaryDown = view.filter((e) => e.dir === 'in' && e.kind === 'binary');
  const pageAcks = view
    .filter((e) => e.dir === 'out' && e.type === 'sentence_ack' && e.msg)
    .map((e) => {
      const epoch = e.msg.epoch;
      const sentenceId = e.msg.sentence_id;
      return {
        epoch, sentenceId,
        playedBeforeAck: playedBeforeAck(playback.trace, epoch, sentenceId),
      };
    });
  return {
    ok: true,
    wsOpened: view.some((e) => e.dir === 'evt' && e.kind === 'open'),
    downBinaryFrames: binaryDown.length,
    downBinaryBytes: binaryDown.reduce((n, e) => n + (e.bytes || 0), 0),
    pageAcks,
    playback,
    mic: await micInfo(page),
  };
}

const SCENARIOS = { fixture: scenarioFixture };

async function main() {
  const args = process.argv.slice(2);
  if (args.indexOf('--check') >= 0) {
    const check = checkSelf();
    emit({ ok: check.ok, scenario: 'self-check', selfCheck: check });
    process.exit(check.ok ? 0 : 1);
    return;
  }
  const opt = (name) => {
    const i = args.indexOf(name);
    return i >= 0 ? args[i + 1] : undefined;
  };
  const scenarioName = opt('--scenario') || 'fixture';
  const url = opt('--url');
  const timeoutMs = Number(opt('--timeout-ms') || 45000);
  const run = SCENARIOS[scenarioName];
  if (!url || !run) {
    emit({ ok: false, error: 'usage: driver.js --url URL [--scenario NAME] [--timeout-ms N] | --check' });
    process.exit(2);
  }

  const browser = await chromium.launch({ headless: true, args: LAUNCH_ARGS });
  let result;
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    await installRtObserver(page);
    await installFakeMic(page);
    result = await run({ page, url, timeoutMs });
    await context.close();
  } catch (err) {
    result = { ok: false, error: String((err && err.message) || err) };
  } finally {
    await browser.close().catch(() => {});
  }
  result.scenario = scenarioName;
  emit(result);
  process.exit(result.ok ? 0 : 1);
}

main().catch((err) => {
  emit({ ok: false, error: String((err && err.message) || err) });
  process.exit(1);
});
