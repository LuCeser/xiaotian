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
const { installFakeMic, micInfo, playPcm16 } = require('../../../../tests/e2e-m1-browser/helpers/fake-mic');

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

// inspectExpectedRound checks one connection's one announcement without
// changing the page. The caller supplies the exact subtitle texts before the
// browser starts; sentence IDs, epoch and turn ID must then agree across the
// observed production frames. A binary frame counts for a sentence only when
// it arrived between that sentence's start and end controls.
function inspectExpectedRound(view, playback, expectedSentences) {
  const reject = (reason) => ({ ok: false, reason });
  if (!Array.isArray(expectedSentences) || expectedSentences.length === 0
      || expectedSentences.some((text) => typeof text !== 'string' || text.length === 0)) {
    return reject('expected sentence list is empty or invalid');
  }
  const opens = view.filter((e) => e.dir === 'evt' && e.kind === 'open');
  if (opens.length !== 1) return reject('expected exactly one WebSocket connection, got ' + opens.length);
  const connection = view.filter((e) => e.i > opens[0].i);
  const teachers = connection.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence' && e.msg);
  if (teachers.length !== expectedSentences.length) {
    return reject('teacher sentences = ' + teachers.length + ', want ' + expectedSentences.length);
  }

  const first = teachers[0].msg;
  const epoch = first.epoch;
  const turnId = first.turn_id;
  const seen = new Set();
  const sentences = [];
  let lastAckIndex = -1;
  for (let n = 0; n < expectedSentences.length; n++) {
    const teacher = teachers[n];
    const msg = teacher.msg;
    if (msg.text !== expectedSentences[n]) {
      return reject('sentence ' + n + ' text = ' + JSON.stringify(msg.text)
        + ', want ' + JSON.stringify(expectedSentences[n]));
    }
    if (msg.epoch !== epoch || msg.turn_id !== turnId || !msg.sentence_id || seen.has(msg.sentence_id)) {
      return reject('sentence ' + n + ' does not belong to one unique epoch/turn round');
    }
    seen.add(msg.sentence_id);
    const matches = (e, dir, type) => e.dir === dir && e.type === type && e.msg
      && e.msg.epoch === epoch && e.msg.turn_id === turnId && e.msg.sentence_id === msg.sentence_id;
    const starts = connection.filter((e) => matches(e, 'in', 'audio_start'));
    const ends = connection.filter((e) => matches(e, 'in', 'audio_end'));
    const acks = connection.filter((e) => e.dir === 'out' && e.type === 'sentence_ack' && e.msg
      && e.msg.epoch === epoch && e.msg.sentence_id === msg.sentence_id);
    if (starts.length !== 1 || ends.length !== 1 || acks.length !== 1) {
      return reject('sentence ' + n + ' controls start/end/ack = '
        + starts.length + '/' + ends.length + '/' + acks.length + ', want 1/1/1');
    }
    const start = starts[0];
    const end = ends[0];
    const ack = acks[0];
    const pcm = connection.filter((e) => e.dir === 'in' && e.kind === 'binary'
      && e.i > start.i && e.i < end.i);
    if (!(teacher.i < start.i && start.i < end.i && end.i < ack.i) || pcm.length === 0) {
      return reject('sentence ' + n + ' lacks ordered teacher/start/PCM/end/ack evidence');
    }
    if (!playedBeforeAck(playback.trace, epoch, msg.sentence_id)) {
      return reject('sentence ' + n + ' ACK did not follow native playback completion');
    }
    lastAckIndex = Math.max(lastAckIndex, ack.i);
    sentences.push({
      text: msg.text, sentenceId: msg.sentence_id,
      pcmFrames: pcm.length, nativeCompletionBeforeAck: true,
    });
  }

  const listening = connection.filter((e) => e.dir === 'in' && e.type === 'state'
    && e.msg && e.msg.value === 'listening' && e.i > teachers[0].i);
  if (listening.some((e) => e.i < lastAckIndex)) {
    return reject('listening appeared before the final expected ACK');
  }
  const restored = listening.find((e) => e.i > lastAckIndex);
  if (!restored) return reject('listening did not follow the final expected ACK');
  return {
    ok: true, connectionOpenIndex: opens[0].i, epoch, turnId,
    sentences, listeningIndex: restored.i, lastAckIndex,
  };
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
  const event = (i, dir, type, msg, kind = 'text') => ({ i, dir, type, msg, kind });
  const expected = ['第一句。', '第二句。'];
  const complete = [event(0, 'evt', undefined, null, 'open')];
  const trace = [];
  let i = 1;
  expected.forEach((sentence, n) => {
    const sid = 's' + n;
    complete.push(event(i++, 'in', 'teacher_sentence',
      { epoch: 7, turn_id: 9, sentence_id: sid, text: sentence }));
    complete.push(event(i++, 'in', 'audio_start', { epoch: 7, turn_id: 9, sentence_id: sid }));
    complete.push(event(i++, 'in', undefined, null, 'binary'));
    complete.push(event(i++, 'in', 'audio_end', { epoch: 7, turn_id: 9, sentence_id: sid }));
    trace.push('onended:7:' + sid, 'ack:7:' + sid);
    complete.push(event(i++, 'out', 'sentence_ack', { epoch: 7, sentence_id: sid }));
  });
  complete.push(event(i++, 'in', 'state', { value: 'listening' }));
  const valid = inspectExpectedRound(complete, { trace }, expected).ok;
  const missingRejected = !inspectExpectedRound(
    complete.filter((e) => !(e.type === 'teacher_sentence' && e.msg.text === expected[1])),
    { trace }, expected).ok;
  const early = complete.slice();
  early.splice(4, 0, event(4.5, 'in', 'state', { value: 'listening' }));
  const earlyListeningRejected = !inspectExpectedRound(early, { trace }, expected).ok;
  if (!valid) violations.push('完整预期轮次未被观察断言接受');
  if (!missingRejected) violations.push('漏句未被观察断言拒绝');
  if (!earlyListeningRejected) violations.push('提前 listening 未被观察断言拒绝');
  return {
    ok: violations.length === 0,
    scenarios,
    micSource: 'fake-mic.js (synthetic getUserMedia, helpers 既有工具)',
    observer: 'rt-observe.js (read-only WebSocket/playback observation)',
    observationAssertions: { valid, missingRejected, earlyListeningRejected },
    violations,
  };
}

// scenario "fixture"：真实页面连接 /rt，点击「开始上课」，等待页面原生播放
// 完成整段教师开场语音——每句都真实发出 sentence_ack（页面的 out 帧，只读
// 观察），且服务器在全部 ACK 后恢复了 listening 状态（in state 帧），说明这
// 是真实的自然完成，不是发送完成或超时捷径。结果里逐句报告
// playedBeforeAck（该句原生 onended 是否先于其 ACK——空播 ACK/丢块 ACK 在
// 此必为 false）与合成麦克风被页面采用的情况。
async function scenarioFixture({ page, url, timeoutMs, expectedSentences, preWaitMs = 0 }) {
  await page.goto(url);
  await page.locator('#rt-start').click();
  if (preWaitMs > 0) await page.waitForTimeout(preWaitMs);
  try {
    await waitWs(page, async (view) => inspectExpectedRound(
      view, await playbackState(page), expectedSentences).ok,
    { timeoutMs, label: 'complete expected opening playback before listening is restored' });
  } catch (err) {
    // 诊断增补：超时原语只带 trace 尾部，这里补齐 droppedChunks/麦克风状态，
    // 让「帧已到、播放停滞」类失败可分辨。
    let diag = '';
    try {
      diag = JSON.stringify({ playback: await playbackState(page), mic: await micInfo(page) });
    } catch (_) { /* page may be gone */ }
    throw new Error(String(err && err.message) + '\ndiag: ' + diag);
  }

  const view = await wsView(page);
  const playback = await playbackState(page);
  const round = inspectExpectedRound(view, playback, expectedSentences);
  if (!round.ok) throw new Error('expected playback observation rejected: ' + round.reason);
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
    round,
    playback,
    mic: await micInfo(page),
  };
}

async function scenarioHeldFixture(args) {
  return scenarioFixture({ ...args, preWaitMs: args.holdMs });
}

async function scenarioCloseAfterOpening(args) {
  const opening = await scenarioFixture(args);
  if (args.holdMs > 0) await args.page.waitForTimeout(args.holdMs);
  await args.page.locator('#rt-end').click();
  await waitWs(args.page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs: args.timeoutMs, label: 'original WebSocket close after end_session failure' });
  const view = await wsView(args.page);
  return {
    ...opening,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
  };
}

async function scenarioScreenVerdictClose(args) {
  const opening = await scenarioFixture(args);
  const before = (await wsView(args.page)).filter((e) => e.dir === 'in' && e.type === 'check_verdicts').length;
  await args.page.locator('.rt-check-row .rt-check-btn').first().click();
  await waitWs(args.page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs: args.timeoutMs, label: 'close while screen verdict is saving' });
  const view = await wsView(args.page);
  return {
    ...opening,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateVerdictEchoes: view.filter((e) => e.dir === 'in' && e.type === 'check_verdicts').length - before,
  };
}

// tonePcm16Base64 — 生成一段确定性音频剪辑（voice-input.spec.js 的同款生成
// 器）：2 秒 440 Hz s16le @16 kHz，能量足够触发服务端 VAD、长度覆盖一个分段
// （+ 拖尾）。播放走 fake-mic.js 的合成麦克风（playPcm16）——这是「提供麦克
// 风来源」的法定能力：只向合成流注入音频，页面生产采集链路无感知，驱动器
// 仍不接触 WS 或页面内部。
function tonePcm16Base64(ms = 2000, freq = 440, rate = 16000, amp = 0.6) {
  const n = Math.round((ms * rate) / 1000);
  const buf = Buffer.alloc(n * 2);
  for (let i = 0; i < n; i++) {
    buf.writeInt16LE(Math.round(Math.sin((2 * Math.PI * freq * i) / rate) * amp * 32767), i * 2);
  }
  return buf.toString('base64');
}

// scenario "voice-verdict-close"（1.07b）：检查阶段的自然语音入口。恢复播报
// 自然完成并回到 listening 后，向合成麦克风播放一段真实音调——生产采集→
// 上行→服务端 VAD→ASR→检查分类→语音判定全部真实发生；Go 侧在真实判定写入
// 的持久化门上关闭会话。页面只观察：close 事件、关闭后迟到的 check_verdicts
// 回显与教师播报必须为零，语音轮的 state 轨迹里必须出现 processing（证明这
// 是真的语音轮，不是测试代劳）。
async function scenarioVoiceVerdictClose(args) {
  const opening = await scenarioFixture(args);
  const before = await wsView(args.page);
  const echoesBefore = before.filter((e) => e.dir === 'in' && e.type === 'check_verdicts').length;
  const teachersBefore = before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length;
  await playPcm16(args.page, tonePcm16Base64(), 16000, 0.6);
  await waitWs(args.page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs: args.timeoutMs, label: 'close while the spoken verdict is saving' });
  const view = await wsView(args.page);
  const voiceStates = view
    .filter((e) => e.dir === 'in' && e.type === 'state' && e.msg && e.i > opening.round.lastAckIndex)
    .map((e) => e.msg.value);
  return {
    ...opening,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateVerdictEchoes: view.filter((e) => e.dir === 'in' && e.type === 'check_verdicts').length - echoesBefore,
    lateTeacherSentences: view.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length - teachersBefore,
    voiceStates,
  };
}

// scenario "starve-probe"：点击「开始上课」后只观察一个固定窗口
// （--hold-ms，默认 3000），输出窗口内页面真实收到的下行控制/二进制帧数
// 后退出，不等待播放完成。这是故障控制点「命中」的页面侧证据：被控制的
// 方向（控制帧／音频帧／全部）在窗口内必须为零；解除控制后的自然完成由
// 其它 scenario 验证。
async function scenarioStarveProbe({ page, url, holdMs }) {
  await page.goto(url);
  await page.locator('#rt-start').click();
  await page.waitForTimeout(holdMs);
  const view = await wsView(page);
  return {
    ok: true,
    wsOpened: view.some((e) => e.dir === 'evt' && e.kind === 'open'),
    holdControlIn: view.filter((e) => e.dir === 'in' && e.kind === 'text').length,
    holdBinaryIn: view.filter((e) => e.dir === 'in' && e.kind === 'binary').length,
  };
}

const SCENARIOS = {
  fixture: scenarioFixture,
  'held-fixture': scenarioHeldFixture,
  'close-after-opening': scenarioCloseAfterOpening,
  'screen-verdict-close': scenarioScreenVerdictClose,
  'voice-verdict-close': scenarioVoiceVerdictClose,
  'starve-probe': scenarioStarveProbe,
};

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
  const holdMs = Number(opt('--hold-ms') || 3000);
  let expectedSentences;
  try {
    expectedSentences = JSON.parse(opt('--expected-sentences-json') || 'null');
  } catch (err) {
    emit({ ok: false, error: 'invalid --expected-sentences-json: ' + String(err.message || err) });
    process.exit(2);
  }
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
    result = await run({ page, url, timeoutMs, holdMs, expectedSentences });
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
