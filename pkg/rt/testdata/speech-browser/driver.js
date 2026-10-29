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
// changing the page. The caller supplies the exact subtitle texts and the
// expected PCM chunk count per sentence (the fixture's own TTS shape) before
// the browser starts; sentence IDs, epoch and turn ID must then agree across
// the observed production frames. A binary frame counts for a sentence only
// when it arrived between that sentence's start and end controls — and the
// observation REJECTS both a window that lost chunks (缺块) and any binary
// frame outside every window (乱序: a chunk that raced its own audio_start or
// trailed its audio_end).
function inspectExpectedRound(view, playback, expectedSentences, expectedPcmPerSentence) {
  const reject = (reason) => ({ ok: false, reason });
  if (!Array.isArray(expectedSentences) || expectedSentences.length === 0
      || expectedSentences.some((text) => typeof text !== 'string' || text.length === 0)) {
    return reject('expected sentence list is empty or invalid');
  }
  if (!Number.isInteger(expectedPcmPerSentence) || expectedPcmPerSentence < 1) {
    return reject('expected PCM chunks per sentence is missing or invalid');
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
  const windows = [];
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
    if (!(teacher.i < start.i && start.i < end.i && end.i < ack.i)) {
      return reject('sentence ' + n + ' lacks ordered teacher/start/PCM/end/ack evidence');
    }
    if (pcm.length !== expectedPcmPerSentence) {
      return reject('sentence ' + n + ' PCM chunks in its start/end window = ' + pcm.length
        + ', want exactly ' + expectedPcmPerSentence + ' — a missing chunk is not natural playback');
    }
    if (!playedBeforeAck(playback.trace, epoch, msg.sentence_id)) {
      return reject('sentence ' + n + ' ACK did not follow native playback completion');
    }
    windows.push({ start: start.i, end: end.i });
    lastAckIndex = Math.max(lastAckIndex, ack.i);
    sentences.push({
      text: msg.text, sentenceId: msg.sentence_id,
      pcmFrames: pcm.length, nativeCompletionBeforeAck: true,
    });
  }

  // 乱序: every binary frame of the round (anything before the final expected
  // ACK) must sit strictly inside exactly one sentence window — a chunk that
  // raced ahead of its audio_start or trailed its audio_end is a transport
  // ordering defect, not something the page may silently absorb.
  const strays = connection.filter((e) => e.dir === 'in' && e.kind === 'binary' && e.i < lastAckIndex
    && !windows.some((w) => w.start < e.i && e.i < w.end));
  if (strays.length > 0) {
    return reject(strays.length + ' PCM chunk(s) landed outside every sentence start/end window (misordered transport)');
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
  const pcmPerSentence = 2;
  const complete = [event(0, 'evt', undefined, null, 'open')];
  const trace = [];
  let i = 1;
  expected.forEach((sentence, n) => {
    const sid = 's' + n;
    complete.push(event(i++, 'in', 'teacher_sentence',
      { epoch: 7, turn_id: 9, sentence_id: sid, text: sentence }));
    complete.push(event(i++, 'in', 'audio_start', { epoch: 7, turn_id: 9, sentence_id: sid }));
    complete.push(event(i++, 'in', undefined, null, 'binary'));
    complete.push(event(i++, 'in', undefined, null, 'binary'));
    complete.push(event(i++, 'in', 'audio_end', { epoch: 7, turn_id: 9, sentence_id: sid }));
    trace.push('onended:7:' + sid, 'ack:7:' + sid);
    complete.push(event(i++, 'out', 'sentence_ack', { epoch: 7, sentence_id: sid }));
  });
  complete.push(event(i++, 'in', 'state', { value: 'listening' }));
  const valid = inspectExpectedRound(complete, { trace }, expected, pcmPerSentence).ok;
  const missingRejected = !inspectExpectedRound(
    complete.filter((e) => !(e.type === 'teacher_sentence' && e.msg.text === expected[1])),
    { trace }, expected, pcmPerSentence).ok;
  // 缺块: sentence 2's window keeps only one of its two PCM chunks.
  const secondStart = complete.findIndex((e, idx) => idx > 0 && e.type === 'audio_start' && e.msg.sentence_id === 's1');
  const chunkMissing = complete.filter((e, idx) => !(idx === secondStart + 2 && e.kind === 'binary'));
  const chunkMissingRejected = !inspectExpectedRound(chunkMissing, { trace }, expected, pcmPerSentence).ok;
  // 乱序: one of sentence 2's chunks is moved AHEAD of its audio_start.
  const chunkMisordered = complete.filter((e, idx) => !(idx === secondStart + 2 && e.kind === 'binary'));
  chunkMisordered.splice(secondStart, 0, event(secondStart - 0.5, 'in', undefined, null, 'binary'));
  const chunkMisorderedRejected = !inspectExpectedRound(chunkMisordered, { trace }, expected, pcmPerSentence).ok;
  const early = complete.slice();
  early.splice(4, 0, event(4.5, 'in', 'state', { value: 'listening' }));
  const earlyListeningRejected = !inspectExpectedRound(early, { trace }, expected, pcmPerSentence).ok;
  if (!valid) violations.push('完整预期轮次未被观察断言接受');
  if (!missingRejected) violations.push('漏句未被观察断言拒绝');
  if (!chunkMissingRejected) violations.push('缺块轮次未被观察断言拒绝');
  if (!chunkMisorderedRejected) violations.push('乱序轮次未被观察断言拒绝');
  if (!earlyListeningRejected) violations.push('提前 listening 未被观察断言拒绝');
  return {
    ok: violations.length === 0,
    scenarios,
    micSource: 'fake-mic.js (synthetic getUserMedia, helpers 既有工具)',
    observer: 'rt-observe.js (read-only WebSocket/playback observation)',
    observationAssertions: { valid, missingRejected, chunkMissingRejected, chunkMisorderedRejected, earlyListeningRejected },
    violations,
  };
}

// scenario "fixture"：真实页面连接 /rt，点击「开始上课」，等待页面原生播放
// 完成整段教师开场语音——每句都真实发出 sentence_ack（页面的 out 帧，只读
// 观察），且服务器在全部 ACK 后恢复了 listening 状态（in state 帧），说明这
// 是真实的自然完成，不是发送完成或超时捷径。结果里逐句报告
// playedBeforeAck（该句原生 onended 是否先于其 ACK——空播 ACK/丢块 ACK 在
// 此必为 false）与合成麦克风被页面采用的情况。
async function scenarioFixture({ page, url, timeoutMs, expectedSentences, expectedPcmPerSentence, preWaitMs = 0 }) {
  await page.goto(url);
  await page.locator('#rt-start').click();
  if (preWaitMs > 0) await page.waitForTimeout(preWaitMs);
  try {
    await waitWs(page, async (view) => inspectExpectedRound(
      view, await playbackState(page), expectedSentences, expectedPcmPerSentence).ok,
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
  const round = inspectExpectedRound(view, playback, expectedSentences, expectedPcmPerSentence);
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

// notebookJpegB64 — 一张极小的真实 JPEG（64×64 色块）。页面生产链路经
// Image/canvas 解码并重编码后分块上传；服务端 fake OCR 只看脚本应答，不看
// 像素。文件必须可解码：解码失败会走 encode-failed，不上传。
const notebookJpegB64 = ''
  + '/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAYEBQYFBAYGBQYHBwYIChAKCgkJChQODwwQFxQY'
  + 'GBcUFhYaHSUfGhsjHBYWICwgIyYnKSopGR8tMC0oMCUoKSj/2wBDAQcHBwoIChMKChMoGhYa'
  + 'KCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCj/wAAR'
  + 'CABAAEADASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAA'
  + 'AgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkK'
  + 'FhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWG'
  + 'h4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl'
  + '5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREA'
  + 'AgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYk'
  + 'NOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOE'
  + 'hYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk'
  + '5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwD12iiiuQoKKKKACiiigAooooAKKKKACiii'
  + 'gAooooAKKKKACiiigAooooAKKKKACiiigAooooAKKKKACiiigAooooA//9k=';

// scenario "ocr-prefill-close"（1.08）：检查阶段的真实拍照入口。恢复播报自然
// 完成后，点击「拍照检查」并经隐藏 file input 投入一张真实 JPEG——页面生产
// 链路（canvas 降采样 → 分块 photo_chunk 控制 frame）真实上传，服务端 OCR →
// 预填批次写入真实发生；Go 侧在选定的控制点（预填写入门／OCR 调用保持）上
// 关闭会话。页面只观察：close 事件与关闭后迟到的 check_verdicts、ocr_result、
// 教师播报必须为零。
async function scenarioOcrPrefillClose(args) {
  const opening = await scenarioFixture(args);
  const before = await wsView(args.page);
  const echoesBefore = before.filter((e) => e.dir === 'in' && e.type === 'check_verdicts').length;
  const ocrBefore = before.filter((e) => e.dir === 'in' && e.type === 'ocr_result').length;
  const teachersBefore = before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length;
  await args.page.locator('.rt-check-photo').click();
  await args.page.locator('input[type=file]').setInputFiles({
    name: 'notebook.jpg',
    mimeType: 'image/jpeg',
    buffer: Buffer.from(notebookJpegB64, 'base64'),
  });
  await waitWs(args.page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs: args.timeoutMs, label: 'close while the OCR prefill batch is saving' });
  const view = await wsView(args.page);
  return {
    ...opening,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateVerdictEchoes: view.filter((e) => e.dir === 'in' && e.type === 'check_verdicts').length - echoesBefore,
    lateOcrResults: view.filter((e) => e.dir === 'in' && e.type === 'ocr_result').length - ocrBefore,
    lateTeacherSentences: view.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length - teachersBefore,
  };
}

// scenario "check-submit-close"（1.09）：真实正式提交入口。恢复播报自然完成、
// 全部词都带人工判定（服务端 echo 驱动）后，点击「检查完了，提交」——生产
// check_submit → CompleteCheck 锁存 → 保存与回执真实发生；Go 侧在提交的控
// 制点（实际提交前／提交已落库未返回）上关闭会话。页面只观察：close 事件、
// 迟到的 review_result 与总结播报必须为零。
async function scenarioCheckSubmitClose(args) {
  const opening = await scenarioFixture(args);
  const before = await wsView(args.page);
  const teachersBefore = before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length;
  const reviewBefore = before.filter((e) => e.dir === 'in' && e.type === 'review_result').length;
  await args.page.locator('.rt-check-submit').click();
  await waitWs(args.page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs: args.timeoutMs, label: 'close racing the formal check submit' });
  const view = await wsView(args.page);
  return {
    ...opening,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateTeacherSentences: view.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length - teachersBefore,
    lateReviewResults: view.filter((e) => e.dir === 'in' && e.type === 'review_result').length - reviewBefore,
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

// scenario "resume-close-window"（1.13a）：连接并开始课堂后不等播报，只等
// 服务端关闭（关闭由 Go 侧在 resume 呈现窗口的两侧触发），报告 close 计
// 数与关闭前到达的 check_started / teacher_sentence 帧数——「呈现胜出窗
// 口仍获兜底」由这些计数证明呈现确实发生，「关闭赢窗口拒绝呈现」由它们
// 归零证明，close 之后再无任何下行帧。
async function scenarioResumeCloseWindow(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs, label: 'server close from the resume presentation window' });
  const view = await wsView(page);
  const closeAt = view.findIndex((e) => e.dir === 'evt' && e.kind === 'close');
  const before = closeAt >= 0 ? view.slice(0, closeAt) : [];
  const after = closeAt >= 0 ? view.slice(closeAt + 1) : [];
  return {
    ok: true,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    checkStarted: before.filter((e) => e.dir === 'in' && e.type === 'check_started').length,
    teacherSentences: before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length,
    lateFrames: after.filter((e) => e.dir === 'in').length,
  };
}

// scenario "correction-submit-close"（1.10a 复验）：重连补检课堂——恢复播
// 报自然完成后，先点第一行的「错」与第二行的「对」（人工改判，凑出最小
// diff），再点「检查完了，提交」；关闭由 Go 侧在提交闩驻留时触发，页面只
// 观察 close 与迟到的 review_result/教师句。--submit-delay-ms 在改判与提交
// 之间留一个固定间隔（密封顺序复验用：Go 侧在该窗口内先落关闭）。
async function scenarioCorrectionSubmitClose(args) {
  const opening = await scenarioFixture(args);
  const before = await wsView(args.page);
  const teachersBefore = before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length;
  const reviewBefore = before.filter((e) => e.dir === 'in' && e.type === 'review_result').length;
  const buttons = args.page.locator('.rt-check-row .rt-check-btn');
  await buttons.nth(1).click(); // 第一行的「错」—— the one-word diff this submit carries
  await buttons.nth(3).click(); // 第二行的「错」—— agrees with the derived hint: no diff
  if (args.submitDelayMs > 0) await args.page.waitForTimeout(args.submitDelayMs);
  await args.page.locator('.rt-check-submit').click();
  await waitWs(args.page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs: args.timeoutMs, label: 'close racing the correction submit' });
  const view = await wsView(args.page);
  return {
    ...opening,
    verdictTaps: view.filter((e) => e.dir === 'out' && e.type === 'check_verdict').length,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateTeacherSentences: view.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length - teachersBefore,
    lateReviewResults: view.filter((e) => e.dir === 'in' && e.type === 'review_result').length - reviewBefore,
  };
}

// scenario "passage-submit-close"（1.10d 复验）：课文背默课堂（?passage=
// &scope= 打开本范围的行）。任务播报自然 ACK 后，向合成麦克风播放一段真
// 实音调——生产采集→VAD→ASR→confirm 全真实——背默任务一次确认即进检查；
// 桥接播报落后面板点「对」，再点提交；关闭由 Go 侧在提交闩驻留时触发。
// --submit-delay-ms 在改判与提交之间留固定间隔（密封顺序复验用）。
async function scenarioPassageSubmitClose(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url + '?passage=77&scope=51');
  await page.locator('#rt-start').click();
  await waitWs(page, async (view) => inspectExpectedRound(view, await playbackState(page), args.expectedSentences, args.expectedPcmPerSentence).ok,
    { timeoutMs, label: 'passage greeting played naturally' });
  await playPcm16(page, tonePcm16Base64());
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'check_started'),
    { timeoutMs, label: 'spoken confirm opened the passage check' });
  const before = await wsView(page);
  const teachersBefore = before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length;
  const reviewBefore = before.filter((e) => e.dir === 'in' && e.type === 'review_result').length;
  await page.locator('.rt-check-row .rt-check-btn').first().click();
  if (args.submitDelayMs > 0) await args.page.waitForTimeout(args.submitDelayMs);
  await page.locator('.rt-check-submit').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs, label: 'close racing the passage submit' });
  const view = await wsView(page);
  return {
    ok: true,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateTeacherSentences: view.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length - teachersBefore,
    lateReviewResults: view.filter((e) => e.dir === 'in' && e.type === 'review_result').length - reviewBefore,
  };
}

// scenario "poem-submit-close"（1.10c 复验）：古诗背默课堂（?poem= 打开整
// 首背默行）。任务播报自然 ACK 后播放一段真实音调——生产采集→VAD→ASR→
// confirm——整首诗一次确认进检查；面板点两句「对」，再点提交；关闭由 Go
// 侧在提交闩驻留时触发。--submit-delay-ms 在改判与提交之间留固定间隔。
async function scenarioPoemSubmitClose(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url + '?poem=77');
  await page.locator('#rt-start').click();
  await waitWs(page, async (view) => inspectExpectedRound(view, await playbackState(page), args.expectedSentences, args.expectedPcmPerSentence).ok,
    { timeoutMs, label: 'poem greeting played naturally' });
  await playPcm16(page, tonePcm16Base64());
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'check_started'),
    { timeoutMs, label: 'spoken confirm opened the poem check' });
  const before = await wsView(page);
  const teachersBefore = before.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length;
  const reviewBefore = before.filter((e) => e.dir === 'in' && e.type === 'review_result').length;
  const buttons = args.page.locator('.rt-check-row .rt-check-btn');
  await buttons.nth(0).click(); // 第一句「对」
  await buttons.nth(2).click(); // 第二句「对」
  if (args.submitDelayMs > 0) await args.page.waitForTimeout(args.submitDelayMs);
  await args.page.locator('.rt-check-submit').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'evt' && e.kind === 'close'),
    { timeoutMs, label: 'close racing the poem submit' });
  const view = await wsView(page);
  return {
    ok: true,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateTeacherSentences: view.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length - teachersBefore,
    lateReviewResults: view.filter((e) => e.dir === 'in' && e.type === 'review_result').length - reviewBefore,
  };
}

// waitSentenceAcked：等一句精确文本的 teacher_sentence 到达且其原生
// sentence_ack 已发出（自然 ACK 的逐句判据，scenario 内部复用）。前缀版
// 用于随机词序的句子（加练选词随机，词文本不可预知）。
async function waitSentenceAcked(page, text, timeoutMs, label) {
  return waitSentenceAckedBy(page, (t) => t === text, timeoutMs, label);
}

// ---------------------------------------------------------------------------
// S4（issue #69 rethink3）：课堂切换竞态的只读观察辅助。换课在同一连接上
// 发生（同一条 WebSocket 内的第二个 session_init），页面/播放器全程真实；
// 这些辅助只把「轮次」组织成可断言的登记结果。
// ---------------------------------------------------------------------------

// teacherRounds 把一条连接上的 teacher_sentence 帧按 turn_id 分组成轮次
// （每次播报一个 turn id；换课 bump epoch，后续播报骑新 turn id）。
function teacherRounds(view) {
  const opens = view.filter((e) => e.dir === 'evt' && e.kind === 'open');
  if (!opens.length) return [];
  const connection = view.filter((e) => e.i > opens[0].i);
  const teachers = connection.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence' && e.msg);
  const rounds = [];
  let current = null;
  for (const e of teachers) {
    if (!current || current.turnId !== e.msg.turn_id) {
      current = { turnId: e.msg.turn_id, epoch: e.msg.epoch, teacherEvents: [], texts: [] };
      rounds.push(current);
    }
    current.teacherEvents.push(e);
    current.texts.push(e.msg.text);
  }
  return rounds;
}

// roundSentenceFacts gathers one round sentence's wire facts: its single
// audio_start/audio_end inside the round, the binary chunks that landed in
// the start/end window, and the page's out sentence_ack with its native
// onended precedence (playedBeforeAck — the player's own registration, never
// a frame count alone).
function roundSentenceFacts(view, playback, round, teacher) {
  const m = teacher.msg;
  const matches = (e, dir, type) => e.dir === dir && e.type === type && e.msg
    && e.msg.epoch === round.epoch && e.msg.turn_id === round.turnId
    && e.msg.sentence_id === m.sentence_id;
  const starts = view.filter((e) => matches(e, 'in', 'audio_start'));
  const ends = view.filter((e) => matches(e, 'in', 'audio_end'));
  const acks = view.filter((e) => e.dir === 'out' && e.type === 'sentence_ack' && e.msg
    && e.msg.epoch === round.epoch && e.msg.sentence_id === m.sentence_id);
  const start = starts[0];
  const end = ends[0];
  const ack = acks[0];
  const pcmInWindow = start && end
    ? view.filter((e) => e.dir === 'in' && e.kind === 'binary' && e.i > start.i && e.i < end.i).length
    : 0;
  return {
    sentenceId: m.sentence_id,
    starts: starts.length, ends: ends.length, acks: acks.length,
    pcmInWindow,
    startIndex: start ? start.i : -1,
    endIndex: end ? end.i : -1,
    ordered: !!(start && end && ack && teacher.i < start.i && start.i < end.i && end.i < ack.i),
    playedBeforeAck: acks.length === 1 && playedBeforeAck(playback.trace, round.epoch, m.sentence_id),
    firstIndex: teacher.i, lastIndex: ack ? ack.i : (end || start || teacher).i,
  };
}

// inspectRound checks one observed round against its expected texts and (when
// expectedPcmPerSentence is a number) the fixture's PCM shape: every sentence
// 1 start / 1 end / exactly the expected chunk count in its window / exactly
// one ack that followed its native onended, in that order; and no binary
// chunk outside every window inside the round's span. Returns the
// per-sentence facts either way — a REJECTED round (the player dropped a
// switched classroom's audio) reports its facts honestly instead of throwing,
// so the Go side asserts on the registration.
function inspectRound(round, view, playback, expectedTexts, expectedPcmPerSentence) {
  const facts = { epoch: round.epoch, turnId: round.turnId, texts: round.texts.slice(), sentences: [] };
  const problems = [];
  if (JSON.stringify(round.texts) !== JSON.stringify(expectedTexts)) {
    problems.push('texts=' + JSON.stringify(round.texts) + ' want ' + JSON.stringify(expectedTexts));
  }
  for (const teacher of round.teacherEvents) {
    const f = roundSentenceFacts(view, playback, round, teacher);
    facts.sentences.push(f);
    if (f.starts !== 1 || f.ends !== 1) problems.push(f.sentenceId + ' start/end=' + f.starts + '/' + f.ends);
    if (f.acks !== 1) problems.push(f.sentenceId + ' acks=' + f.acks);
    if (typeof expectedPcmPerSentence === 'number' && f.pcmInWindow !== expectedPcmPerSentence) {
      problems.push(f.sentenceId + ' pcm=' + f.pcmInWindow + ' want ' + expectedPcmPerSentence);
    }
    if (!f.ordered) problems.push(f.sentenceId + ' frame order broken');
    if (f.acks === 1 && !f.playedBeforeAck) problems.push(f.sentenceId + ' ack without native onended');
  }
  const spanStart = round.teacherEvents.length ? round.teacherEvents[0].i : 0;
  const spanEnd = facts.sentences.length
    ? Math.max(...facts.sentences.map((f) => f.lastIndex))
    : spanStart;
  const strays = view.filter((e) => e.dir === 'in' && e.kind === 'binary'
    && e.i > spanStart && e.i < spanEnd
    && !facts.sentences.some((f) => f.startIndex >= 0 && f.endIndex >= 0 && f.startIndex < e.i && e.i < f.endIndex));
  if (strays.length) problems.push(strays.length + ' stray chunk(s) outside every window');
  facts.ok = problems.length === 0;
  facts.problems = problems;
  facts.allAckedNaturally = facts.sentences.length > 0
    && facts.sentences.every((f) => f.acks === 1 && f.playedBeforeAck && f.ordered);
  return facts;
}

// waitNthRoundAcked waits until the nth round carrying exactly texts has all
// its sentences acked (the wire fact), then returns its full facts with the
// real playback trace (playedBeforeAck checked by the caller's assertions).
async function waitNthRoundAcked(page, texts, nth, timeoutMs, label) {
  await waitWs(page, (view) => {
    const rounds = teacherRounds(view).filter((r) => JSON.stringify(r.texts) === JSON.stringify(texts));
    if (rounds.length < nth) return false;
    const facts = inspectRound(rounds[nth - 1], view, { trace: [] }, texts, null);
    return facts.sentences.length === texts.length && facts.sentences.every((f) => f.acks >= 1);
  }, { timeoutMs, label });
  const view = await wsView(page);
  return inspectRound(
    teacherRounds(view).filter((r) => JSON.stringify(r.texts) === JSON.stringify(texts))[nth - 1],
    view, await playbackState(page), texts, null);
}

// waitSwitchRoundSecond is the classroom-switch wait: the second round either
// completes NATURALLY (every ack followed its onended — the fix present) or,
// once every expected frame has landed on the wire and a rejection margin has
// passed, is judged by the PLAYER'S OWN REGISTRATION — never by the absence
// of acks (review P2-7). A round was refused when NONE of its (epoch,
// sentence) keys appears in the player's ACCEPTANCE HISTORY — every
// audio_start was turned away at the epoch gate, so nothing of the round was
// ever accepted. The live ledger alone cannot decide this (review P2-7 fix2):
// stopNow() — the switch's own interrupt_ack path and the drain watchdog —
// clears the ledger of ALREADY ACCEPTED audio, which would misread
// accepted-but-stopped as refused. A round whose keys ARE in the history but
// that never finished (accepted, then stopped or still queued) is NOT a
// rejection: the wait reports it honestly as an observation failure instead
// of a verdict.
async function waitSwitchRoundSecond(page, texts, pcmPerSentence, rejectMarginMs, timeoutMs) {
  let framesCompleteAt = 0;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const view = await wsView(page);
    const rounds = teacherRounds(view);
    if (rounds.length >= 2 && JSON.stringify(rounds[1].texts) === JSON.stringify(texts)) {
      const playback = await playbackState(page);
      const facts = inspectRound(rounds[1], view, playback, texts, pcmPerSentence);
      if (facts.allAckedNaturally) return { verdict: 'acked', facts };
      const framesComplete = facts.sentences.length === texts.length
        && facts.sentences.every((f) => f.starts === 1 && f.ends === 1 && f.pcmInWindow === pcmPerSentence);
      if (framesComplete && !framesCompleteAt) framesCompleteAt = Date.now();
      if (framesComplete && framesCompleteAt && Date.now() - framesCompleteAt >= rejectMarginMs) {
        const roundKeys = facts.sentences.map((f) => facts.epoch + ':' + f.sentenceId);
        const accepted = roundKeys.filter((k) => playback.acceptedKeys.indexOf(k) >= 0).length;
        if (accepted === 0) {
          // The player processed every announcement of the round and refused
          // each at its epoch gate — no acceptance ever happened.
          facts.rejection = {
            acceptedKeys: 0, ledgerKeys: playback.ledgerKeys.length,
            droppedChunks: playback.droppedChunks,
          };
          return { verdict: 'rejected', facts };
        }
        throw new Error('the switched round was ACCEPTED by the player ('
          + accepted + '/' + roundKeys.length + ' acceptance-history keys) but never completed — '
          + 'accepted-but-stopped-or-unfinished is not a rejection '
          + '(ledger books now ' + playback.ledgerKeys.length + '; stopNow clears them): '
          + JSON.stringify({ acks: facts.sentences.map((f) => f.acks) }));
      }
    }
    if (Date.now() > deadline) {
      throw new Error('the switched classroom\'s round neither completed nor fully landed within '
        + timeoutMs + 'ms');
    }
    await page.waitForTimeout(150);
  }
}

// countIn / frameCounters — tiny tally helpers for the reports.
function frameCount(view, dir, type) {
  return view.filter((e) => e.dir === dir && e.type === type).length;
}

// waitSwitchGateArmed polls the TEST SERVER's switch gate from the DRIVER
// process (never the page, never the socket): the gate arms only after the
// Go side observed every superseded-side server task truly exit, so the late
// window counted afterwards is causally after that exit — a fixed time
// window no longer stands in for the completion relation (review P1-4). The
// bound only limits the observation; on expiry the journey fails honestly.
async function waitSwitchGateArmed(url, boundMs) {
  const gateUrl = new URL('/__switch_gate', url).href;
  const deadline = Date.now() + boundMs;
  for (;;) {
    try {
      const r = await fetch(gateUrl);
      const j = await r.json();
      if (j && j.armed) return;
    } catch (_) { /* the fixture server answers momentarily */ }
    if (Date.now() > deadline) {
      throw new Error('the switch gate never armed within ' + boundMs
        + 'ms — the superseded side did not truly exit first');
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

// waitEarlyCloseEvidence watches POSITIVELY for the released stale close
// having ended the LIVE classroom or its transport (review P2-8): an honored
// stale close ALWAYS ships session_ended and closes the socket — wire facts,
// never a button-click timeout. The bound only limits the watch; plain
// absence lets the journey proceed to B's own submit (the red is the POSITIVE
// detection below, not "waited and nothing came").
async function waitEarlyCloseEvidence(page, baselineIndex, boundMs) {
  const deadline = Date.now() + boundMs;
  for (;;) {
    const view = await wsView(page);
    const after = view.filter((e) => e.i > baselineIndex);
    const ended = after.filter((e) => e.dir === 'in' && e.type === 'session_ended');
    const closed = after.filter((e) => e.dir === 'evt' && e.kind === 'close');
    if (ended.length || closed.length) {
      return {
        sessionEnded: ended.map((e) => (e.msg && e.msg.reason) || ''),
        wsClosed: closed.length,
      };
    }
    if (Date.now() > deadline) return null;
    await page.waitForTimeout(150);
  }
}

// interruptAckFacts lists every inbound interrupt_ack with its wire index.
function interruptAckFacts(view) {
  return view.filter((e) => e.dir === 'in' && e.type === 'interrupt_ack' && e.msg)
    .map((e) => ({ index: e.i, oldEpoch: e.msg.old_epoch, newEpoch: e.msg.new_epoch }));
}

// errorFrameFacts lists every inbound error frame with its wire index.
function errorFrameFacts(view) {
  return view.filter((e) => e.dir === 'in' && e.type === 'error' && e.msg)
    .map((e) => ({ index: e.i, code: e.msg.code || '', recoverable: !!e.msg.recoverable, message: e.msg.message || '' }));
}

// lateFrameCounts tallies the frames that landed after one index (the late
// window's baseline): what followed a settled round must be nothing.
function lateFrameCounts(view, afterIndex) {
  const after = view.filter((e) => e.i > afterIndex);
  return {
    teacherSentences: after.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length,
    reviewResults: after.filter((e) => e.dir === 'in' && e.type === 'review_result').length,
    sessionEnded: after.filter((e) => e.dir === 'in' && e.type === 'session_ended').length,
    sessionReady: after.filter((e) => e.dir === 'in' && e.type === 'session_ready').length,
    interruptAcks: after.filter((e) => e.dir === 'in' && e.type === 'interrupt_ack').length,
    errors: after.filter((e) => e.dir === 'in' && e.type === 'error').length,
  };
}

// roundLastIndex is one round's last observed frame index (its final ack, or
// the last control of its last sentence when the ack never came).
function roundLastIndex(facts) {
  return facts.sentences.length
    ? Math.max(...facts.sentences.map((f) => f.lastIndex))
    : 0;
}

async function waitSentenceAckedBy(page, pred, timeoutMs, label) {
  await waitWs(page, (view) => {
    const s = view.find((e) => e.dir === 'in' && e.type === 'teacher_sentence' && e.msg && pred(e.msg.text));
    if (!s) return false;
    return view.some((e) => e.dir === 'out' && e.type === 'sentence_ack' && e.msg && e.msg.sentence_id === s.msg.sentence_id);
  }, { timeoutMs, label });
}

// scenario "practice-submit-close"（1.10b 复验）：已提交词课堂短路结束
// （session_ended(lesson_complete)）→ banner 的「再练一组」以同一 start 流
// 程开新连接进加练课堂；加练开场与逐词播报全部自然 ACK，两次语音「写了
// 完了」推进（合成音调→生产采集→VAD→ASR→confirm），检查面板两行人工判
// 定后提交；关闭由 Go 侧在提交闩驻留时触发。
async function scenarioPracticeSubmitClose(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'session_ended' && e.msg && e.msg.reason === 'lesson_complete'),
    { timeoutMs, label: 'completed lesson short-circuit' });
  await page.locator('.rt-practice-btn').click();
  await waitSentenceAcked(page, '你好呀！', timeoutMs, 'practice greeting sentence 1');
  await waitSentenceAcked(page, '今天我们有 2 个词要写。', timeoutMs, 'practice greeting sentence 2');
  // Student masking (D-10): the greeting/word subtitles carry the word's
  // POSITION only, never its text — and the picker's order is random.
  await waitSentenceAcked(page, '听好哦：第 1 个词。', timeoutMs, 'practice greeting sentence 3');
  await playPcm16(page, tonePcm16Base64());
  await waitSentenceAcked(page, '第 2 个词。', timeoutMs, 'second practice word announced');
  await playPcm16(page, tonePcm16Base64());
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'check_started'),
    { timeoutMs, label: 'spoken confirm opened the practice check' });
  // The check bridge must finish its natural playback too — the baseline is
  // taken only after it, so late-frame counting cannot absorb it.
  await waitSentenceAcked(page, '全都写完啦，真棒！', timeoutMs, 'practice check bridge sentence 1');
  await waitSentenceAckedBy(page, (t) => t.indexOf('现在我们一起检查：屏幕上有 2 个词') === 0, timeoutMs, 'practice check bridge sentence 2');
  const buttons = page.locator('.rt-check-row .rt-check-btn');
  await buttons.nth(0).click();
  await buttons.nth(2).click();
  await page.locator('.rt-check-submit').click();
  // TWO closes: the completed short-circuit connection (the page dropped it
  // when 再练一组 reconnected) and the practice connection's goodbye —
  // wait for BOTH so the late window starts at the last one.
  await waitWs(page, (view) => view.filter((e) => e.dir === 'evt' && e.kind === 'close').length >= 2,
    { timeoutMs, label: 'both connections closed (short-circuit + practice goodbye)' });
  const view = await wsView(page);
  // TWO connections live in this journey: the completed short-circuit's own
  // close fires FIRST, the practice connection's close last — late frames
  // are counted only past the LAST close event.
  const closeAt = view.map((e) => e.dir === 'evt' && e.kind === 'close').lastIndexOf(true);
  const after = closeAt >= 0 ? view.slice(closeAt + 1) : [];
  return {
    ok: true,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    lateTeacherSentences: after.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').length,
    lateReviewResults: after.filter((e) => e.dir === 'in' && e.type === 'review_result').length,
    lateSentenceTexts: after.filter((e) => e.dir === 'in' && e.type === 'teacher_sentence').map((e) => e.msg && e.msg.text),
  };
}

// scenario "check-submit-natural"（S4）：真实提交的当前课堂成功路径——恢复
// 播报自然完成后点「检查完了，提交」，提交的锁存、FSRS 保存、回执与结果/
// 总结呈现全部自然发生（总结轮自然 ACK，随后 session_ended 与页面关闭）。
// --expect-busy-error 1 时（reinit-during-submit 同体）在提交后先等那条可
// 恢复的同 lessonID 重入错误（Go 侧在锁存驻留时注入重入），再等结果。
async function scenarioCheckSubmitNatural(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  const bridge = await waitNthRoundAcked(page, args.expectedSentences, 1, timeoutMs, 'resume bridge played naturally');
  await page.locator('.rt-check-submit').click();
  if (args.expectBusyError) {
    await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'error' && e.msg
      && e.msg.code === 'lesson_unavailable' && e.msg.recoverable === true),
    { timeoutMs, label: 'the recoverable same-lessonID reentry error' });
  }
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'review_result'),
    { timeoutMs, label: 'the accepted submit\'s review_result' });
  const summary = await waitNthRoundAcked(page, args.expectedSecond, 1, timeoutMs, 'summary played naturally');
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'session_ended')
      && view.some((e) => e.dir === 'evt' && e.kind === 'close'),
  { timeoutMs, label: 'natural lesson_complete ending and close' });
  const view = await wsView(page);
  return {
    ok: true,
    rounds: [bridge, summary],
    readyCount: frameCount(view, 'in', 'session_ready'),
    readyIndexes: view.filter((e) => e.dir === 'in' && e.type === 'session_ready').map((e) => e.i),
    checkStartedCount: frameCount(view, 'in', 'check_started'),
    reviewResultCount: frameCount(view, 'in', 'review_result'),
    sessionEndedCount: frameCount(view, 'in', 'session_ended'),
    interruptAcks: interruptAckFacts(view),
    errorFrames: errorFrameFacts(view),
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
  };
}

// scenario "classroom-switch-opening"（S4）：A 的开场播报进行中（第 1 句已自
// 然 ACK、后续句已排队）同连接换课到 B——页面必须先收到 interrupt_ack（停
// 掉已排队旧音频并采纳新 epoch），再收 B 的 session_ready 与开场音频并自然
// ACK。第二条轮次要么自然完成，要么在全部帧落地后仍被播放器拒收（拒绝的
// 登记结果如实上报，Go 侧据此断言，绝不用消息数量代替）。
async function scenarioClassroomSwitchOpening(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  // Round 1 (A's opening) starts playing; the Go side switches classrooms as
  // soon as it observes the page's first ack, mid-round on purpose.
  await waitWs(page, (view) => frameCount(view, 'out', 'sentence_ack') >= 1,
    { timeoutMs, label: 'the first opening sentence acked naturally' });
  const second = await waitSwitchRoundSecond(page, args.expectedSecond, args.expectedPcmPerSentence,
    args.rejectMarginMs, timeoutMs);
  const view = await wsView(page);
  const playback = await playbackState(page);
  const rounds = teacherRounds(view);
  const round1 = rounds.length ? inspectRound(rounds[0], view, playback, args.expectedSentences, null) : null;
  const readyIndexes = view.filter((e) => e.dir === 'in' && e.type === 'session_ready').map((e) => e.i);
  const firstAck = interruptAckFacts(view)[0];
  return {
    ok: true,
    secondRoundVerdict: second.verdict,
    rounds: [round1, second.facts],
    readyCount: readyIndexes.length,
    readyIndexes,
    interruptAcks: interruptAckFacts(view),
    interruptAckBeforeSecondReady: !!(firstAck && readyIndexes.length >= 2
      && readyIndexes[0] < firstAck.index && firstAck.index < readyIndexes[1]),
    errorFrames: errorFrameFacts(view),
    stopNowCount: playback.trace.filter((t) => t === 'stopNow').length,
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    sessionEndedCount: frameCount(view, 'in', 'session_ended'),
    droppedChunks: playback.droppedChunks,
    mic: await micInfo(page),
  };
}

// scenario "classroom-switch-submit"（S4）：A 的正式提交在锁存前驻留（Go 侧
// 门），同连接注入 B（独立真实行）的 session_init——B 的恢复播报自然完成后
// Go 侧释放 A：A 的保存照常落库（归属 A），呈现被换课 epoch 拒绝（迟到窗内
// 零输出）。随后页面在 B 的面板上再点一次提交：B 的提交走自己的成功路径
// （结果/总结/结束全自然）。
async function scenarioClassroomSwitchSubmit(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  const bridgeA = await waitNthRoundAcked(page, args.expectedSentences, 1, timeoutMs, 'A resume bridge played naturally');
  await page.locator('.rt-check-submit').click();
  // The switched classroom's own resume bridge — the SECOND round carrying
  // these texts (A's bridge and B's bridge speak the same copy).
  const bridgeB = await waitNthRoundAcked(page, args.expectedSecond, 2, timeoutMs, 'B resume bridge played naturally');
  const baseline = roundLastIndex(bridgeB);
  const late0 = lateFrameCounts(await wsView(page), baseline);
  // The late window starts only once the SERVER confirms A's released task
  // truly exited (the gate) — then, and only then, is the window counted.
  await waitSwitchGateArmed(url, args.lateWindowMs);
  const late = lateFrameCounts(await wsView(page), baseline);
  if (late.teacherSentences > late0.teacherSentences
      || late.reviewResults > late0.reviewResults || late.sessionEnded > late0.sessionEnded) {
    // A late A-side result landed inside the window — report it; the Go side
    // owns the assertion (and its own release timing).
    return {
      ok: false,
      error: 'late frames landed inside the switch window: ' + JSON.stringify(late),
      rounds: [bridgeA, bridgeB],
      lateWindow: late,
    };
  }
  // B's own submit through the real panel.
  await page.locator('.rt-check-submit').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'review_result'),
    { timeoutMs, label: 'B\'s own submit review_result' });
  const summary = await waitNthRoundAcked(page, args.expectedThird, 1, timeoutMs, 'B summary played naturally');
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'session_ended')
      && view.some((e) => e.dir === 'evt' && e.kind === 'close'),
  { timeoutMs, label: 'B natural ending and close' });
  const view = await wsView(page);
  return {
    ok: true,
    rounds: [bridgeA, bridgeB, summary],
    lateWindow: late,
    readyCount: frameCount(view, 'in', 'session_ready'),
    readyIndexes: view.filter((e) => e.dir === 'in' && e.type === 'session_ready').map((e) => e.i),
    checkStartedCount: frameCount(view, 'in', 'check_started'),
    reviewResultCount: frameCount(view, 'in', 'review_result'),
    sessionEndedCount: frameCount(view, 'in', 'session_ended'),
    interruptAcks: interruptAckFacts(view),
    errorFrames: errorFrameFacts(view),
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
  };
}

// scenario "subject-ask-switch"（S4）：双科都有到期词 → subject_ask 问题自然
// 播报后，孩子语音答「语文」（真实采集→VAD→ASR→关键词绑定）——A 的续接在
// 取词处驻留（Go 侧按类目门），Go 侧同连接注入 B（english 独立行）：B 安装
// 并自然完成开场。迟到续接释放后必须零输出（无第二个 session_ready、无 A
// 的开场播报、无第二条 epoch 通知）。
async function scenarioSubjectAskSwitch(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  const ask = await waitNthRoundAcked(page, args.expectedSentences, 1, timeoutMs, 'subject ask played naturally');
  await playPcm16(page, tonePcm16Base64(), 16000, 0.6);
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'session_ready'),
    { timeoutMs, label: 'the replacement classroom\'s session_ready' });
  const greeting = await waitNthRoundAcked(page, args.expectedSecond, 1, timeoutMs, 'B greeting played naturally');
  const baseline = roundLastIndex(greeting);
  // The late window starts only once the SERVER confirms the released late
  // continuation truly exited (the gate) — a fixed window no longer decides
  // whether the continuation was done (review P1-4).
  await waitSwitchGateArmed(url, args.lateWindowMs);
  const view = await wsView(page);
  return {
    ok: true,
    rounds: [ask, greeting],
    lateWindow: lateFrameCounts(view, baseline),
    readyCount: frameCount(view, 'in', 'session_ready'),
    readyIndexes: view.filter((e) => e.dir === 'in' && e.type === 'session_ready').map((e) => e.i),
    interruptAcks: interruptAckFacts(view),
    errorFrames: errorFrameFacts(view),
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    droppedChunks: (await playbackState(page)).droppedChunks,
  };
}

// scenario "reinit-same-row"（S4）：同一 lessonID 的重入（无在途保存）——恢复
// 播报自然完成后，同连接再次 session_init 同一行：interrupt_ack（旧→新
// epoch）先于第二个 session_ready，检查恢复的桥接播报骑安装后的新 epoch
// 自然 ACK，连接保持（无结束、无关闭）。
async function scenarioReinitSameRow(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  const first = await waitNthRoundAcked(page, args.expectedSentences, 1, timeoutMs, 'first resume bridge played naturally');
  await waitWs(page, (view) => frameCount(view, 'in', 'session_ready') >= 2,
    { timeoutMs, label: 'the re-init\'s session_ready' });
  const second = await waitNthRoundAcked(page, args.expectedSecond, 2, timeoutMs, 're-init bridge played naturally');
  const baseline = roundLastIndex(second);
  await page.waitForTimeout(args.lateWindowMs);
  const view = await wsView(page);
  return {
    ok: true,
    rounds: [first, second],
    lateWindow: lateFrameCounts(view, baseline),
    readyCount: frameCount(view, 'in', 'session_ready'),
    readyIndexes: view.filter((e) => e.dir === 'in' && e.type === 'session_ready').map((e) => e.i),
    checkStartedCount: frameCount(view, 'in', 'check_started'),
    interruptAcks: interruptAckFacts(view),
    errorFrames: errorFrameFacts(view),
    wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
  };
}

// scenario "reinit-during-submit"（S4）：A 的提交锁存驻留（Go 门）时同连接重
// 入同一 lessonID——按推荐语义必须答一条可恢复的初始化错误（不装旧状态课
// 堂）；随后 A 的保存自然完成（结果/总结自然 ACK、session_ended、关闭）。
async function scenarioReinitDuringSubmit(args) {
  return scenarioCheckSubmitNatural({ ...args, expectBusyError: true });
}

// scenario "stale-completed-close"（S3 真实浏览器验收，review P2-8）：auto
// 入口双科有到期词 → subject_ask 自然播报后孩子语音答「语文」（真实采集
// →VAD→ASR→绑定）——该续接在语音回合协程上读到已完成行并短路重放
// （session_ready），其 scoped 关闭请求驻留在 Go 侧；同连接注入 B（独立
// 真实 english 检查行）：B 安装并自然完成恢复桥（页面真实 ACK——A 驻留
// 在语音协程而非读循环，上行通路保持可用）。Go 侧放行 A 的陈旧关闭并
// 由整任务观察确认其退出后（开关门 armed）页面才统计迟到窗并在 B 的面
// 板上点提交：A 的陈旧关闭不得结束 B、不得提前断开连接；B 自己的提交
// 走完整成功路径。全程不代发 ACK、不改 fake 行造 B。
async function scenarioStaleCompletedClose(args) {
  const { page, url, timeoutMs } = args;
  await page.goto(url);
  await page.locator('#rt-start').click();
  // The ask round plays and acks naturally, then the child answers 语文 by
  // voice — the answered continuation (A) runs on the voice turn's own
  // goroutine from here on.
  const ask = await waitNthRoundAcked(page, args.expectedSentences, 1, timeoutMs, 'subject ask played naturally');
  await playPcm16(page, tonePcm16Base64(), 16000, 0.6);
  await waitWs(page, (view) => frameCount(view, 'in', 'session_ready') >= 1,
    { timeoutMs, label: 'the completed replay\'s session_ready' });
  // B's resume bridge — the FIRST round of these texts (A's completed replay
  // speaks nothing) — plays and acks NATURALLY while A's close request is
  // still parked.
  const bridge = await waitNthRoundAcked(page, args.expectedSecond, 1, timeoutMs, 'B resume bridge played naturally');
  const baseline = roundLastIndex(bridge);
  // The wire facts shared by every exit below (success, business failure
  // and — via main's catch — the exception path: observations are never
  // discarded, review P2-8).
  const wireFacts = async () => {
    const view = await wsView(page);
    const ended = view.filter((e) => e.dir === 'in' && e.type === 'session_ended' && e.msg);
    return {
      rounds: [],
      readyCount: frameCount(view, 'in', 'session_ready'),
      readyIndexes: view.filter((e) => e.dir === 'in' && e.type === 'session_ready').map((e) => e.i),
      checkStartedCount: frameCount(view, 'in', 'check_started'),
      reviewResultCount: frameCount(view, 'in', 'review_result'),
      sessionEndedCount: ended.length,
      endedReasons: ended.map((e) => e.msg.reason || ''),
      interruptAcks: interruptAckFacts(view),
      errorFrames: errorFrameFacts(view),
      wsCloseCount: view.filter((e) => e.dir === 'evt' && e.kind === 'close').length,
    };
  };
  // The stale close is released and the voice turn's TRUE exit confirmed by
  // the server's whole-task observation before the late window opens (the
  // gate): whatever A's close did is on the wire by these counts.
  await waitSwitchGateArmed(url, args.lateWindowMs);
  // Business assertion first (review P2-8): an honored stale close ends B
  // RIGHT HERE — session_ended / early transport close after the bridge are
  // the violation's own wire facts; only their ABSENCE within the watch
  // lets the journey proceed to B's own submit (the red is the positive
  // detection, never a submit-button timeout).
  const early = await waitEarlyCloseEvidence(page, baseline, args.earlyCloseWatchMs);
  const late = lateFrameCounts(await wsView(page), baseline);
  if (early) {
    return Object.assign(await wireFacts(), {
      ok: false,
      error: 'the released stale close ended the live classroom before B\'s own submit: '
        + 'session_ended=' + JSON.stringify(early.sessionEnded) + ' ws_close=' + early.wsClosed,
      earlyClose: early,
      lateWindow: late,
    });
  }
  // B keeps handling messages: its own submit through the REAL panel.
  await page.locator('.rt-check-submit').click();
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'review_result'),
    { timeoutMs, label: 'B\'s own submit review_result' });
  const summary = await waitNthRoundAcked(page, args.expectedThird, 1, timeoutMs, 'B summary played naturally');
  await waitWs(page, (view) => view.some((e) => e.dir === 'in' && e.type === 'session_ended')
      && view.some((e) => e.dir === 'evt' && e.kind === 'close'),
  { timeoutMs, label: 'B natural ending and close' });
  return Object.assign(await wireFacts(), {
    ok: true,
    rounds: [ask, bridge, summary],
    lateWindow: late,
  });
}

const SCENARIOS = {
  fixture: scenarioFixture,
  'held-fixture': scenarioHeldFixture,
  'close-after-opening': scenarioCloseAfterOpening,
  'screen-verdict-close': scenarioScreenVerdictClose,
  'resume-close-window': scenarioResumeCloseWindow,
  'voice-verdict-close': scenarioVoiceVerdictClose,
  'ocr-prefill-close': scenarioOcrPrefillClose,
  'check-submit-close': scenarioCheckSubmitClose,
  'correction-submit-close': scenarioCorrectionSubmitClose,
  'passage-submit-close': scenarioPassageSubmitClose,
  'poem-submit-close': scenarioPoemSubmitClose,
  'practice-submit-close': scenarioPracticeSubmitClose,
  'starve-probe': scenarioStarveProbe,
  'check-submit-natural': scenarioCheckSubmitNatural,
  'classroom-switch-opening': scenarioClassroomSwitchOpening,
  'classroom-switch-submit': scenarioClassroomSwitchSubmit,
  'subject-ask-switch': scenarioSubjectAskSwitch,
  'reinit-same-row': scenarioReinitSameRow,
  'reinit-during-submit': scenarioReinitDuringSubmit,
  'stale-completed-close': scenarioStaleCompletedClose,
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
  const submitDelayMs = Number(opt('--submit-delay-ms') || 0);
  const lateWindowMs = Number(opt('--late-window-ms') || 3000);
  const earlyCloseWatchMs = Number(opt('--early-close-watch-ms') || 3000);
  const rejectMarginMs = Number(opt('--reject-margin-ms') || 8000);
  const expectBusyError = Number(opt('--expect-busy-error') || 0) === 1;
  const expectedPcmPerSentence = Number(opt('--expected-pcm-per-sentence'));
  const parseList = (name) => {
    try {
      return JSON.parse(opt(name) || 'null');
    } catch (err) {
      emit({ ok: false, error: 'invalid ' + name + ': ' + String(err.message || err) });
      process.exit(2);
    }
    return null;
  };
  const expectedSentences = parseList('--expected-sentences-json');
  const expectedSecond = parseList('--expected-second-json');
  const expectedThird = parseList('--expected-third-json');
  const run = SCENARIOS[scenarioName];
  if (!url || !run) {
    emit({ ok: false, error: 'usage: driver.js --url URL [--scenario NAME] [--timeout-ms N] | --check' });
    process.exit(2);
  }

  const browser = await chromium.launch({ headless: true, args: LAUNCH_ARGS });
  let result;
  let observedPage = null;
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    observedPage = page;
    await installRtObserver(page);
    await installFakeMic(page);
    result = await run({ page, url, timeoutMs, holdMs, submitDelayMs, expectedSentences, expectedSecond, expectedThird, expectedPcmPerSentence, lateWindowMs, earlyCloseWatchMs, rejectMarginMs, expectBusyError });
    await context.close();
  } catch (err) {
    result = { ok: false, error: String((err && err.message) || err) };
    // Review P2-8: an exception must not discard the journey's observations
    // — attach the page's wire/player facts best-effort so the failure
    // carries business evidence instead of a bare locator message.
    if (observedPage) {
      try {
        result.observed = {
          wsTail: (await wsView(observedPage)).slice(-40),
          playback: await playbackState(observedPage),
        };
      } catch (_) { /* the page/context may already be gone */ }
    }
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
