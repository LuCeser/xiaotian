#!/usr/bin/env node
'use strict';
/*
 * scripts/rt-timing-report.js — RT 时序证据分析（Phase 13, Plan 07, RT-04/05）。
 *
 * 消费两条证据流并输出时序报告：
 *   1. 客户端 JSONL（pkg/web/static/tutor/js/rt-client.js TimingLog 导出的
 *      rt-timing-*.jsonl，每行 {event, t_ms, ...extra}，t_ms 为页面单调钟）：
 *      page_ready / auth_complete / connected / first_audio_played{turn_id,epoch,
 *      sentence_id} / interrupt_start{epoch,sentence_id,source} /
 *      interrupt_stop{epoch} / clock_offset{offset_ms,rtt_ms}
 *   2. 服务端日志（zap JSON 行，msg="rt_event"，t_mono_ns 为进程单调钟）：
 *      connected / speech_started / speech_ended{turn_id} / tool_start{turn_id,
 *      tool,request_id} / tool_end / first_audio_received{turn_id} / interrupt /
 *      lesson_version{turn_id,version} / session_end / error{code}
 *
 * 「句末→首实际可听」第六周判定口径（M-6，写死）：
 *   以会话内 clock_offset 事件（ping/pong 对称时延估计）的 offset_ms 中位数为
 *   偏移，把服务端 speech_ended(t_mono_ns) 换算到客户端钟后减
 *   first_audio_played，并附 ±RTT/2 误差界（rtt 中位数/2）一并输出。
 *   无 clock_offset 的会话样本不计入达标统计，单独输出计数（诚实降级）。
 *   跨机时钟绝不直接相减——唯一的跨机换算就是上式这一处，且必附误差界。
 *   同钟指标组（服务端 speech_ended→first_audio_received、客户端锚点→
 *   first_audio_played）作为辅助分列输出：客户端无 transcript_received 事件，
 *   锚点按计划回退为 connected 之后、首个 first_audio_played 之前的首个事件。
 *
 * 样本政策（13-CHECKPOINTS 明令）：P50/P95 基于完成样本；失败/超时/无锚点样本
 * 全部显式计数、绝不剔除隐藏。响应失败 = speech_ended 后无 first_audio_played
 * 的轮次；打断失败 = interrupt_start 后无 interrupt_stop。
 *
 * 会话配对：客户端 JSONL 不含 session_id（脱敏边界），按 turn_id 集合的最大
 * 重叠贪心配对；各文件导出时间接近日志窗口时配对唯一，极端同构 turn 集存在
 * 歧义可能（报告中列出配对与重叠数供人工复核）。
 *
 * 用法：
 *   node scripts/rt-timing-report.js <rt-timing-*.jsonl> [更多.jsonl ...] \
 *       [--server-log <服务端日志>]... [--json]
 *   node scripts/rt-timing-report.js --self-test [--json]
 *   npm run rt-report -- <参数同上>
 *
 * 零依赖（stock Node）。退出码：0 正常；1 用法错误或 self-test 断言失败。
 */

const fs = require('node:fs');

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const out = { files: [], serverLogs: [], json: false, selfTest: false, help: false, errors: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--json') {
      out.json = true;
    } else if (a === '--self-test') {
      out.selfTest = true;
    } else if (a === '--help' || a === '-h') {
      out.help = true;
    } else if (a === '--server-log') {
      const next = argv[i + 1];
      if (!next) {
        out.errors.push('--server-log 需要一个文件路径参数');
        i++;
      } else {
        out.serverLogs.push(next);
        i++;
      }
    } else if (a.startsWith('--')) {
      out.errors.push('未知参数: ' + a);
    } else {
      out.files.push(a);
    }
  }
  return out;
}

// ---------------------------------------------------------------------------
// 输入装载
// ---------------------------------------------------------------------------

/** 读一个客户端 JSONL 导出文件为会话（entries 稳定按 t_ms 排序）。 */
function loadClientSession(file) {
  const text = fs.readFileSync(file, 'utf8');
  const entries = [];
  let malformed = 0;
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    let obj;
    try {
      obj = JSON.parse(line);
    } catch (parseErr) {
      malformed += 1;
      continue;
    }
    if (!obj || typeof obj.event !== 'string' || typeof obj.t_ms !== 'number') {
      malformed += 1;
      continue;
    }
    entries.push(obj);
  }
  // TimingLog 按压栈顺序导出；排序仅为防御（稳定排序保持同刻事件的原始顺序）。
  entries.sort((x, y) => x.t_ms - y.t_ms);
  return { source: file, entries, malformed };
}

/**
 * 读一个服务端日志文件，抽出 rt_event 行按 session_id 分组。
 * 每行从首个 '{' 起尝试 JSON 解析（容忍 journalctl 等前缀）；只保留
 * msg==="rt_event"（或等价地带 event+t_mono_ns 字段）的对象。
 */
function loadServerEvents(file) {
  const text = fs.readFileSync(file, 'utf8');
  const sessions = new Map(); // session_id -> events[]
  let rtEventLines = 0;
  let skippedLines = 0;
  for (const raw of text.split('\n')) {
    const brace = raw.indexOf('{');
    if (brace < 0) {
      if (raw.trim()) skippedLines += 1;
      continue;
    }
    let obj;
    try {
      obj = JSON.parse(raw.slice(brace));
    } catch (parseErr) {
      skippedLines += 1;
      continue;
    }
    const isRtEvent = obj && (obj.msg === 'rt_event' ||
      (typeof obj.event === 'string' && typeof obj.t_mono_ns === 'number'));
    if (!isRtEvent || typeof obj.session_id !== 'string') {
      skippedLines += 1;
      continue;
    }
    rtEventLines += 1;
    const sid = obj.session_id;
    if (!sessions.has(sid)) sessions.set(sid, []);
    sessions.get(sid).push(obj);
  }
  return { source: file, sessions, rtEventLines, skippedLines };
}

// ---------------------------------------------------------------------------
// 统计助手
// ---------------------------------------------------------------------------

function median(xs) {
  if (!xs.length) return null;
  const s = xs.slice().sort((a, b) => a - b);
  const mid = Math.floor(s.length / 2);
  if (s.length % 2 === 1) return s[mid];
  return (s[mid - 1] + s[mid]) / 2;
}

/** 线性插值分位数（numpy 默认口径）；空集返回 null。 */
function percentile(xs, p) {
  if (!xs.length) return null;
  const s = xs.slice().sort((a, b) => a - b);
  if (s.length === 1) return s[0];
  const idx = (p / 100) * (s.length - 1);
  const lo = Math.floor(idx);
  const hi = Math.ceil(idx);
  if (lo === hi) return s[lo];
  return s[lo] + (idx - lo) * (s[hi] - s[lo]);
}

function round1(x) {
  return (x === null || x === undefined || Number.isNaN(x)) ? null : Math.round(x * 10) / 10;
}

// ---------------------------------------------------------------------------
// 核心分析
// ---------------------------------------------------------------------------

/** 客户端会话预聚合。 */
function summarizeClient(sess) {
  const s = {
    source: sess.source,
    malformed: sess.malformed,
    audioByTurn: new Map(), // turn_id -> t_ms（每轮首个 first_audio_played）
    greeting: null, // {ok, ms} | {ok:false, reason}
    interruptDurations: [],
    interruptFailedNoStop: 0,
    orphanStops: 0,
    offsets: [],
    rtts: [],
    auxClientSample: null, // connected 后首事件 -> 首个 first_audio_played
    counts: {}
  };
  let auth = null;
  let connected = null;
  let firstAudio = null;
  let openInterrupt = null;
  for (const e of sess.entries) {
    s.counts[e.event] = (s.counts[e.event] || 0) + 1;
    switch (e.event) {
      case 'auth_complete':
        if (auth === null) auth = e;
        break;
      case 'connected':
        if (connected === null) connected = e;
        break;
      case 'first_audio_played':
        if (firstAudio === null) firstAudio = e;
        if (e.turn_id !== undefined && !s.audioByTurn.has(e.turn_id)) {
          s.audioByTurn.set(e.turn_id, e.t_ms);
        }
        break;
      case 'clock_offset':
        if (typeof e.offset_ms === 'number') s.offsets.push(e.offset_ms);
        if (typeof e.rtt_ms === 'number') s.rtts.push(e.rtt_ms);
        break;
      case 'interrupt_start':
        if (openInterrupt === null) openInterrupt = e;
        break;
      case 'interrupt_stop':
        if (openInterrupt !== null) {
          s.interruptDurations.push(e.t_ms - openInterrupt.t_ms);
          openInterrupt = null;
        } else {
          s.orphanStops += 1;
        }
        break;
      default:
        break;
    }
  }
  if (openInterrupt !== null) s.interruptFailedNoStop += 1;

  // 首次问候：auth_complete -> 其后首个 first_audio_played（客户端钟）。
  if (auth === null) {
    s.greeting = { ok: false, reason: '缺 auth_complete 事件' };
  } else if (firstAudio === null || firstAudio.t_ms < auth.t_ms) {
    s.greeting = { ok: false, reason: 'auth_complete 后无 first_audio_played（首问候从未可听）' };
  } else {
    s.greeting = { ok: true, ms: firstAudio.t_ms - auth.t_ms };
  }

  // 辅助同钟锚点（回退口径）：connected 之后、首个 first_audio_played 之前的
  // 首个事件；两者之间无事件则无锚点（不计样本，单独计数）。
  if (connected !== null && firstAudio !== null) {
    for (const e of sess.entries) {
      if (e.t_ms > connected.t_ms && e.t_ms < firstAudio.t_ms) {
        s.auxClientSample = { anchorEvent: e.event, ms: firstAudio.t_ms - e.t_ms };
        break;
      }
    }
  }
  return s;
}

/** 服务端会话预聚合（同一 session_id 的 rt_event 流，共享一个进程单调锚）。 */
function summarizeServer(events) {
  const s = {
    speechEndedByTurn: new Map(), // turn_id -> t_mono_ns（首个）
    firstAudioByTurn: new Map(), // turn_id -> t_mono_ns（首个）
    toolDurations: [], // {tool, requestId, ms}
    toolsByTool: new Map(), // tool -> ms[]
    openTools: new Map(), // request_id -> {tool, startNs}
    lessonVersions: [], // {turnId, version}
    errorCodes: [], // code
    interruptsHandled: 0
  };
  for (const e of events) {
    switch (e.event) {
      case 'speech_ended':
        if (e.turn_id !== undefined && !s.speechEndedByTurn.has(e.turn_id)) {
          s.speechEndedByTurn.set(e.turn_id, Number(e.t_mono_ns));
        }
        break;
      case 'first_audio_received':
        if (e.turn_id !== undefined && !s.firstAudioByTurn.has(e.turn_id)) {
          s.firstAudioByTurn.set(e.turn_id, Number(e.t_mono_ns));
        }
        break;
      case 'tool_start':
        if (e.request_id) s.openTools.set(e.request_id, { tool: e.tool || '?', startNs: Number(e.t_mono_ns) });
        break;
      case 'tool_end': {
        const open = e.request_id ? s.openTools.get(e.request_id) : null;
        if (open) {
          const ms = (Number(e.t_mono_ns) - open.startNs) / 1e6;
          s.toolDurations.push({ tool: open.tool, requestId: e.request_id, ms });
          if (!s.toolsByTool.has(open.tool)) s.toolsByTool.set(open.tool, []);
          s.toolsByTool.get(open.tool).push(ms);
          s.openTools.delete(e.request_id);
        }
        break;
      }
      case 'lesson_version':
        s.lessonVersions.push({ turnId: e.turn_id, version: e.version });
        break;
      case 'error':
        s.errorCodes.push(e.code || 'unknown');
        break;
      case 'interrupt':
        s.interruptsHandled += 1;
        break;
      default:
        break;
    }
  }
  return s;
}

/** 客户端会话 ↔ 服务端会话配对：按 turn_id 集合最大重叠贪心（各用一次）。 */
function matchSessions(clientSummaries, serverSummaries) {
  const pairs = new Array(clientSummaries.length).fill(null);
  const used = new Set();
  clientSummaries.forEach((cs, i) => {
    let best = null;
    let bestOverlap = 0;
    for (const ss of serverSummaries) {
      if (used.has(ss.sessionId)) continue;
      let overlap = 0;
      for (const turn of cs.audioByTurn.keys()) {
        if (ss.sum.speechEndedByTurn.has(turn)) overlap += 1;
      }
      if (overlap > bestOverlap) {
        bestOverlap = overlap;
        best = ss;
      }
    }
    if (best) {
      used.add(best.sessionId);
      pairs[i] = { client: cs, server: best, overlap: bestOverlap };
    }
  });
  return pairs;
}

/** 全量分析：输入已解析的客户端会话与服务端会话（可为 null），输出可 JSON 化结果。 */
function analyze(clientSessions, serverSessionsByFile) {
  const clientSummaries = clientSessions.map(summarizeClient);
  const serverSummaries = [];
  if (serverSessionsByFile) {
    for (const f of serverSessionsByFile) {
      for (const [sid, events] of f.sessions) {
        serverSummaries.push({ source: f.source, sessionId: sid, sum: summarizeServer(events) });
      }
    }
  }

  const pairs = matchSessions(clientSummaries, serverSummaries);
  const matchedServerIds = new Set(pairs.filter(Boolean).map((p) => p.server.sessionId));

  const result = {
    meta: {
      clientSessions: clientSummaries.length,
      serverSessions: serverSummaries.length,
      matchedPairs: pairs.filter(Boolean).length,
      malformedClientLines: clientSummaries.reduce((n, c) => n + c.malformed, 0),
      policy: 'P50/P95 基于完成样本；失败/无offset/无锚点样本显式计数不剔除；跨机时刻仅经 clock_offset 中位数校准换算并附 ±RTT/2 误差界'
    },
    pairs: pairs.map((p) => p && {
      client: p.client.source, server: p.server.sessionId, overlap: p.overlap
    }),
    unmatched: {
      client: clientSummaries.filter((c) => !pairs.some((p) => p && p.client === c)).map((c) => c.source),
      server: serverSummaries.filter((s) => !matchedServerIds.has(s.sessionId)).map((s) => s.sessionId)
    },
    greeting: { perSession: [], n: 0, failures: 0, p50: null, p95: null },
    sentenceToAudible: {
      calibrated: { n: 0, p50: null, p95: null, rttMedianMs: null, boundMs: null },
      noOffsetTurns: 0,
      noServerDataSessions: 0,
      auxServer: { n: 0, p50: null, p95: null },
      auxClient: { n: 0, p50: null, p95: null, noAnchorSessions: 0, anchorFallback: 'connected 后、首个 first_audio_played 之前的首个事件（无 transcript_received 事件的诚实降级锚点）' }
    },
    response: { turnsTotal: 0, completedWithAudio: 0, failedNoAudio: 0, serverErrorEvents: 0, errorCodes: {}, calibratedN: 0, clientOnlyAudioTurns: 0 },
    interrupt: { starts: 0, completed: 0, failedNoStop: 0, orphanStops: 0, serverHandled: 0, p50: null, p95: null },
    tools: { perTool: [], lessonVersions: [] }
  };

  // --- 首次问候（每客户端会话一条，客户端钟） ---
  const greetingSamples = [];
  for (const cs of clientSummaries) {
    if (cs.greeting.ok) {
      greetingSamples.push(cs.greeting.ms);
      result.greeting.perSession.push({ session: cs.source, ok: true, ms: round1(cs.greeting.ms) });
    } else {
      result.greeting.failures += 1;
      result.greeting.perSession.push({ session: cs.source, ok: false, reason: cs.greeting.reason });
    }
  }
  result.greeting.n = greetingSamples.length;
  result.greeting.p50 = round1(percentile(greetingSamples, 50));
  result.greeting.p95 = round1(percentile(greetingSamples, 95));

  // --- 打断（全部客户端会话：纯客户端钟配对；服务端 handled 汇总自配对会话） ---
  const interruptSamples = [];
  for (const cs of clientSummaries) {
    for (const d of cs.interruptDurations) interruptSamples.push(d);
    result.interrupt.starts += cs.interruptDurations.length + cs.interruptFailedNoStop;
    result.interrupt.completed += cs.interruptDurations.length;
    result.interrupt.failedNoStop += cs.interruptFailedNoStop;
    result.interrupt.orphanStops += cs.orphanStops;
  }
  result.interrupt.p50 = round1(percentile(interruptSamples, 50));
  result.interrupt.p95 = round1(percentile(interruptSamples, 95));

  // --- 配对驱动的指标：句末→首可听（校准 + 辅助同钟）、响应样本、工具 ---
  const calibrated = [];
  const auxServer = [];
  const auxClient = [];
  const pooledRtt = [];
  const toolMsByTool = new Map();
  const lessonVersions = [];
  const errorCodeCounts = {};

  for (const pair of pairs) {
    if (!pair) continue;
    const cs = pair.client;
    const ss = pair.server.sum;
    const offsetMedian = cs.offsets.length ? median(cs.offsets) : null;
    if (offsetMedian !== null) pooledRtt.push(...cs.rtts);

    for (const [turn, endedNs] of ss.speechEndedByTurn) {
      result.response.turnsTotal += 1;
      const audioT = cs.audioByTurn.get(turn);
      if (audioT === undefined) {
        result.response.failedNoAudio += 1; // 失败样本：保留计数，不剔除
        continue;
      }
      result.response.completedWithAudio += 1;
      if (offsetMedian === null) {
        result.sentenceToAudible.noOffsetTurns += 1; // 单独计数，不入达标统计
      } else {
        // 唯一的跨机换算点：server_ms - offset 换算到客户端钟，附 ±RTT/2 误差界。
        const endedClientEq = endedNs / 1e6 - offsetMedian;
        calibrated.push(audioT - endedClientEq);
      }
      const recvNs = ss.firstAudioByTurn.get(turn);
      if (recvNs !== undefined) auxServer.push((recvNs - endedNs) / 1e6); // 同一进程钟，直接相减
    }
    for (const turn of cs.audioByTurn.keys()) {
      if (!ss.speechEndedByTurn.has(turn)) result.response.clientOnlyAudioTurns += 1;
    }
    for (const code of ss.errorCodes) {
      result.response.serverErrorEvents += 1;
      errorCodeCounts[code] = (errorCodeCounts[code] || 0) + 1;
    }
    result.interrupt.serverHandled += ss.interruptsHandled;
    if (cs.auxClientSample) auxClient.push(cs.auxClientSample.ms);
    else result.sentenceToAudible.auxClient.noAnchorSessions += 1;
    for (const [tool, arr] of ss.toolsByTool) {
      if (!toolMsByTool.has(tool)) toolMsByTool.set(tool, []);
      toolMsByTool.get(tool).push(...arr);
    }
    lessonVersions.push(...ss.lessonVersions);
  }
  result.sentenceToAudible.noServerDataSessions = pairs.filter((p) => !p).length;

  result.sentenceToAudible.calibrated.n = calibrated.length;
  result.sentenceToAudible.calibrated.p50 = round1(percentile(calibrated, 50));
  result.sentenceToAudible.calibrated.p95 = round1(percentile(calibrated, 95));
  const rttMedian = pooledRtt.length ? median(pooledRtt) : null;
  result.sentenceToAudible.calibrated.rttMedianMs = round1(rttMedian);
  result.sentenceToAudible.calibrated.boundMs = round1(rttMedian === null ? null : rttMedian / 2);

  result.sentenceToAudible.auxServer.n = auxServer.length;
  result.sentenceToAudible.auxServer.p50 = round1(percentile(auxServer, 50));
  result.sentenceToAudible.auxServer.p95 = round1(percentile(auxServer, 95));
  result.sentenceToAudible.auxClient.n = auxClient.length;
  result.sentenceToAudible.auxClient.p50 = round1(percentile(auxClient, 50));
  result.sentenceToAudible.auxClient.p95 = round1(percentile(auxClient, 95));

  result.response.calibratedN = calibrated.length;
  result.response.errorCodes = errorCodeCounts;

  for (const [tool, arr] of toolMsByTool) {
    result.tools.perTool.push({
      tool,
      n: arr.length,
      p50: round1(percentile(arr, 50)),
      p95: round1(percentile(arr, 95))
    });
  }
  result.tools.perTool.sort((a, b) => a.tool.localeCompare(b.tool));
  result.tools.lessonVersions = lessonVersions;

  return result;
}

// ---------------------------------------------------------------------------
// 控制台渲染
// ---------------------------------------------------------------------------

function num(x) {
  return x === null ? 'n/a' : String(x);
}

function render(result) {
  const L = [];
  L.push('=== RT 时序证据分析 (rt-timing-report) ===');
  L.push('客户端会话 ' + result.meta.clientSessions + ' | 服务端会话 ' + result.meta.serverSessions +
    ' | 配对 ' + result.meta.matchedPairs + ' | 客户端坏行 ' + result.meta.malformedClientLines);
  L.push('口径: 客户端 t_ms=页面单调钟, 服务端 t_mono_ns=进程单调钟; 跨机时刻仅经 clock_offset 中位数校准换算并附 ±RTT/2 误差界, 绝不直接相减。');
  L.push('样本政策: ' + result.meta.policy);
  L.push('');

  if (result.pairs && result.pairs.length) {
    L.push('[会话配对 (turn_id 集合最大重叠)]');
    for (const p of result.pairs) {
      if (p) L.push('  ' + p.client + '  <->  ' + p.server + '  (重叠 ' + p.overlap + ' 轮)');
    }
    if (result.unmatched.client.length) L.push('  未配对客户端会话: ' + result.unmatched.client.join(', '));
    if (result.unmatched.server.length) L.push('  未配对服务端会话: ' + result.unmatched.server.join(', '));
    L.push('');
  }

  L.push('[首次问候 auth_complete -> first_audio_played (客户端钟)]');
  for (const g of result.greeting.perSession) {
    L.push('  ' + g.session.padEnd(28) + (g.ok ? '  ' + num(g.ms) + ' ms' : '  失败: ' + g.reason));
  }
  L.push('  合计: n=' + result.greeting.n + ' 失败=' + result.greeting.failures +
    ' P50=' + num(result.greeting.p50) + 'ms P95=' + num(result.greeting.p95) + 'ms');
  L.push('');

  L.push('[句末 -> 首实际可听 (RT-04 第六周判定口径)]');
  const c = result.sentenceToAudible.calibrated;
  if (c.n > 0) {
    L.push('  clock_offset 校准端到端 (speech_ended 换算到客户端钟 -> first_audio_played):');
    L.push('    n=' + c.n + ' P50=' + num(c.p50) + 'ms P95=' + num(c.p95) +
      'ms  误差界 ±RTT/2=±' + num(c.boundMs) + 'ms (中位 RTT ' + num(c.rttMedianMs) + 'ms)');
  } else {
    L.push('  clock_offset 校准端到端: n=0 (无可算样本)');
  }
  if (result.sentenceToAudible.noOffsetTurns > 0) {
    L.push('  无 clock_offset 会话样本: ' + result.sentenceToAudible.noOffsetTurns +
      ' 轮 (未计入上方达标统计, 单独计数)');
  }
  if (result.sentenceToAudible.noServerDataSessions > 0) {
    L.push('  无服务端配对会话: ' + result.sentenceToAudible.noServerDataSessions + ' 个 (跨机指标不可算)');
  }
  L.push('  辅助同钟对照 (不跨机相减):');
  L.push('    服务端 speech_ended -> first_audio_received: n=' + result.sentenceToAudible.auxServer.n +
    ' P50=' + num(result.sentenceToAudible.auxServer.p50) + 'ms P95=' + num(result.sentenceToAudible.auxServer.p95) + 'ms');
  L.push('    客户端 (connected 后首事件) -> first_audio_played: n=' + result.sentenceToAudible.auxClient.n +
    ' P50=' + num(result.sentenceToAudible.auxClient.p50) + 'ms P95=' + num(result.sentenceToAudible.auxClient.p95) +
    'ms' + (result.sentenceToAudible.auxClient.noAnchorSessions ? ' (无锚点会话 ' + result.sentenceToAudible.auxClient.noAnchorSessions + ')' : ''));
  L.push('');

  L.push('[响应样本 (每轮 speech_ended 为一轮, 失败样本保留计数)]');
  L.push('  speech_ended 总轮数=' + result.response.turnsTotal +
    ' 完成(有音频)=' + result.response.completedWithAudio +
    ' 失败(无音频)=' + result.response.failedNoAudio +
    ' 服务端 error 事件=' + result.response.serverErrorEvents +
    ' 客户端独有音频轮=' + result.response.clientOnlyAudioTurns);
  L.push('  校准可算 n=' + result.response.calibratedN +
    ' 无offset单独计=' + result.sentenceToAudible.noOffsetTurns +
    ' 无音频失败=' + result.response.failedNoAudio);
  const ec = Object.entries(result.response.errorCodes);
  if (ec.length) L.push('  error 明细: ' + ec.map(([k, v]) => k + ' x' + v).join(', '));
  L.push('');

  L.push('[打断 interrupt_start -> interrupt_stop (客户端钟)]');
  L.push('  start 总数=' + result.interrupt.starts +
    ' 完成=' + result.interrupt.completed +
    ' 失败(无stop)=' + result.interrupt.failedNoStop +
    ' 孤儿stop=' + result.interrupt.orphanStops +
    ' 服务端 handled=' + result.interrupt.serverHandled);
  L.push('  P50=' + num(result.interrupt.p50) + 'ms P95=' + num(result.interrupt.p95) + 'ms (基于完成样本; 失败计数见上, 未剔除)');
  L.push('');

  L.push('[服务端工具执行 (tool_start -> tool_end, 按 request_id 配对)]');
  if (result.tools.perTool.length) {
    L.push('  tool                 n   P50ms   P95ms');
    for (const t of result.tools.perTool) {
      L.push('  ' + t.tool.padEnd(20) + String(t.n).padStart(2) + '  ' +
        num(t.p50).padStart(6) + '  ' + num(t.p95).padStart(6));
    }
  } else {
    L.push('  (无工具事件)');
  }
  const lv = result.tools.lessonVersions;
  L.push('  lesson_version 事件: ' + lv.length +
    (lv.length ? ' (versions: ' + lv.map((v) => v.version).join(', ') + ')' : ''));
  return L.join('\n');
}

// ---------------------------------------------------------------------------
// --self-test：内置样例覆盖两个分支（有/无 clock_offset 会话）与失败样本
// ---------------------------------------------------------------------------

function selfTestData() {
  // 会话 A：含 clock_offset（中位 offset=102ms, 中位 RTT=84ms -> 误差界 ±42ms）。
  const a = { source: 'selftest-a.jsonl', malformed: 0, entries: [
    { event: 'page_ready', t_ms: 0 },
    { event: 'auth_complete', t_ms: 500, turn_id: 0 },
    { event: 'connected', t_ms: 600 },
    { event: 'clock_offset', t_ms: 650, offset_ms: 100, rtt_ms: 80 },
    { event: 'first_audio_played', t_ms: 1500, turn_id: 1, epoch: 0, sentence_id: 's1' },
    { event: 'clock_offset', t_ms: 1600, offset_ms: 104, rtt_ms: 88 },
    { event: 'first_audio_played', t_ms: 5000, turn_id: 2, epoch: 0, sentence_id: 's4' },
    { event: 'interrupt_start', t_ms: 6000, epoch: 0, sentence_id: 's5', source: 'rms' },
    { event: 'interrupt_stop', t_ms: 6120, epoch: 0 },
    { event: 'first_audio_played', t_ms: 7000, turn_id: 3, epoch: 1, sentence_id: 's7' },
    { event: 'interrupt_start', t_ms: 8000, epoch: 1, source: 'rms' } // 无 stop -> 失败样本
  ] };
  // 会话 B：无 clock_offset（其轮次必须被单独计数，不入校准统计）。
  const b = { source: 'selftest-b.jsonl', malformed: 0, entries: [
    { event: 'page_ready', t_ms: 0 },
    { event: 'auth_complete', t_ms: 400 },
    { event: 'connected', t_ms: 500 },
    { event: 'first_audio_played', t_ms: 1400, turn_id: 1, epoch: 0, sentence_id: 's1' },
    { event: 'interrupt_start', t_ms: 3000, epoch: 0, sentence_id: 's2', source: 'rms' },
    { event: 'interrupt_stop', t_ms: 3100, epoch: 0 }
  ] };
  // 服务端日志（含 journalctl 风格前缀行、无关 JSON 行与纯文本坏行，验证抽取健壮性）。
  // server_ms = client_ms + 102（会话 A 中位 offset），据此放置 t_mono_ns。
  const rtEvent = (obj) => JSON.stringify(Object.assign({ level: 'INFO', msg: 'rt_event' }, obj));
  const logLines = [
    'Oct 02 23:00:00 host home-tutor-agent[123]: ' + rtEvent({ event: 'connected', session_id: 'rt-1', t_mono_ns: 650000000 }),
    'not a json line at all',
    '{"msg":"static interface directory verified","dir":"/opt/hta/pkg/web/static"}',
    rtEvent({ event: 'speech_started', session_id: 'rt-1', t_mono_ns: 640000000, turn_id: 1 }),
    rtEvent({ event: 'speech_ended', session_id: 'rt-1', t_mono_ns: 702000000, turn_id: 1 }),
    rtEvent({ event: 'tool_start', session_id: 'rt-1', t_mono_ns: 710000000, turn_id: 1, tool: 'get_lesson_state', request_id: 'call_a1' }),
    rtEvent({ event: 'tool_end', session_id: 'rt-1', t_mono_ns: 730000000, turn_id: 1, tool: 'get_lesson_state', request_id: 'call_a1' }),
    rtEvent({ event: 'first_audio_received', session_id: 'rt-1', t_mono_ns: 1352500000, turn_id: 1 }),
    rtEvent({ event: 'speech_ended', session_id: 'rt-1', t_mono_ns: 4302000000, turn_id: 2 }),
    rtEvent({ event: 'first_audio_received', session_id: 'rt-1', t_mono_ns: 4802000000, turn_id: 2 }),
    rtEvent({ event: 'interrupt', session_id: 'rt-1', t_mono_ns: 6010000000, old_epoch: 0, new_epoch: 1, last_sentence_id: 's5', played_until_ms: 120 }),
    rtEvent({ event: 'speech_ended', session_id: 'rt-1', t_mono_ns: 6102000000, turn_id: 3 }),
    rtEvent({ event: 'tool_start', session_id: 'rt-1', t_mono_ns: 6200000000, turn_id: 3, tool: 'advance_word', request_id: 'call_b2' }),
    rtEvent({ event: 'tool_end', session_id: 'rt-1', t_mono_ns: 6350000000, turn_id: 3, tool: 'advance_word', request_id: 'call_b2' }),
    rtEvent({ event: 'lesson_version', session_id: 'rt-1', t_mono_ns: 6350000000, turn_id: 3, version: 2 }),
    rtEvent({ event: 'first_audio_received', session_id: 'rt-1', t_mono_ns: 6502000000, turn_id: 3 }),
    rtEvent({ event: 'interrupt', session_id: 'rt-1', t_mono_ns: 8000000000, old_epoch: 1, new_epoch: 2 }),
    rtEvent({ event: 'speech_ended', session_id: 'rt-1', t_mono_ns: 8102000000, turn_id: 4 }), // 无音频 -> 失败
    rtEvent({ event: 'error', session_id: 'rt-1', t_mono_ns: 8600000000, code: 'provider_failed' }),
    rtEvent({ event: 'speech_ended', session_id: 'rt-1', t_mono_ns: 9102000000, turn_id: 5 }), // 无音频 -> 失败
    rtEvent({ event: 'session_end', session_id: 'rt-1', t_mono_ns: 9500000000 }),
    rtEvent({ event: 'connected', session_id: 'rt-2', t_mono_ns: 100000000 }),
    rtEvent({ event: 'speech_ended', session_id: 'rt-2', t_mono_ns: 300000000, turn_id: 1 }),
    rtEvent({ event: 'first_audio_received', session_id: 'rt-2', t_mono_ns: 800000000, turn_id: 1 }),
    rtEvent({ event: 'interrupt', session_id: 'rt-2', t_mono_ns: 2950000000, old_epoch: 0, new_epoch: 1 })
  ].join('\n');
  const serverSessions = parseServerText('selftest-server.log', logLines);
  return { clientSessions: [a, b], serverSessions: [serverSessions] };
}

/** 与 loadServerEvents 同构，但直接吃文本（self-test 用）。 */
function parseServerText(source, text) {
  const sessions = new Map();
  let rtEventLines = 0;
  let skippedLines = 0;
  for (const raw of text.split('\n')) {
    const brace = raw.indexOf('{');
    if (brace < 0) { if (raw.trim()) skippedLines += 1; continue; }
    let obj;
    try { obj = JSON.parse(raw.slice(brace)); } catch (parseErr) { skippedLines += 1; continue; }
    const isRtEvent = obj && (obj.msg === 'rt_event' ||
      (typeof obj.event === 'string' && typeof obj.t_mono_ns === 'number'));
    if (!isRtEvent || typeof obj.session_id !== 'string') { skippedLines += 1; continue; }
    rtEventLines += 1;
    if (!sessions.has(obj.session_id)) sessions.set(obj.session_id, []);
    sessions.get(obj.session_id).push(obj);
  }
  return { source, sessions, rtEventLines, skippedLines };
}

function approx(actual, expected, eps, label, failures) {
  const ok = actual !== null && Math.abs(actual - expected) <= eps;
  if (!ok) failures.push(label + ': 期望 ' + expected + ' ±' + eps + ', 实际 ' + actual);
  return ok;
}

function exactEq(actual, expected, label, failures) {
  if (actual !== expected) failures.push(label + ': 期望 ' + expected + ', 实际 ' + actual);
}

function selfTest() {
  const data = selfTestData();
  const result = analyze(data.clientSessions, data.serverSessions);
  const failures = [];

  // 分支覆盖：会话 A（有 clock_offset）与 会话 B（无）都必须走到。
  exactEq(result.meta.clientSessions, 2, '客户端会话数', failures);
  exactEq(result.meta.serverSessions, 2, '服务端会话数', failures);
  exactEq(result.meta.matchedPairs, 2, '配对数', failures);
  exactEq(data.serverSessions[0].rtEventLines, 23, 'rt_event 行数', failures);
  exactEq(data.serverSessions[0].skippedLines, 2, '非事件行跳过数', failures);

  // 首次问候：两会话各 1000ms。
  exactEq(result.greeting.n, 2, '问候样本数', failures);
  exactEq(result.greeting.failures, 0, '问候失败数', failures);
  approx(result.greeting.p50, 1000, 0.01, '问候 P50', failures);

  // 校准端到端（第六周口径）：[800, 900, 1000] -> P50=900, P95=990, 界 ±42。
  exactEq(result.sentenceToAudible.calibrated.n, 3, '校准样本数', failures);
  approx(result.sentenceToAudible.calibrated.p50, 900, 0.01, '校准 P50', failures);
  approx(result.sentenceToAudible.calibrated.p95, 990, 0.01, '校准 P95', failures);
  approx(result.sentenceToAudible.calibrated.boundMs, 42, 0.01, '校准误差界', failures);
  // 无 clock_offset 会话（B 的 1 轮）：单独计数，不入校准统计。
  exactEq(result.sentenceToAudible.noOffsetTurns, 1, '无offset轮数', failures);

  // 响应样本：6 轮 speech_ended，2 轮失败（无音频），1 个服务端 error。
  exactEq(result.response.turnsTotal, 6, '响应总轮数', failures);
  exactEq(result.response.failedNoAudio, 2, '响应失败数', failures);
  exactEq(result.response.serverErrorEvents, 1, '服务端error数', failures);
  exactEq(result.response.completedWithAudio, 4, '响应完成数', failures);

  // 辅助同钟：服务端 4 样本 [400,500,500,650.5]；客户端锚点 1 样本 850（B 无锚点）。
  exactEq(result.sentenceToAudible.auxServer.n, 4, '服务端同钟样本数', failures);
  approx(result.sentenceToAudible.auxServer.p50, 500, 0.01, '服务端同钟 P50', failures);
  approx(result.sentenceToAudible.auxServer.p95, 627.9, 0.01, '服务端同钟 P95', failures);
  exactEq(result.sentenceToAudible.auxClient.n, 1, '客户端锚点样本数', failures);
  approx(result.sentenceToAudible.auxClient.p50, 850, 0.01, '客户端锚点样本值', failures);
  exactEq(result.sentenceToAudible.auxClient.noAnchorSessions, 1, '无锚点会话数', failures);

  // 打断：3 start，2 完成（120/100），1 失败（无 stop），服务端 handled 3。
  exactEq(result.interrupt.starts, 3, '打断 start 总数', failures);
  exactEq(result.interrupt.completed, 2, '打断完成数', failures);
  exactEq(result.interrupt.failedNoStop, 1, '打断失败数', failures);
  approx(result.interrupt.p50, 110, 0.01, '打断 P50', failures);
  approx(result.interrupt.p95, 119, 0.01, '打断 P95', failures);
  exactEq(result.interrupt.serverHandled, 3, '服务端 handled', failures);

  // 工具与 lesson_version：get_lesson_state 20ms / advance_word 150ms / version 2。
  const gs = result.tools.perTool.find((t) => t.tool === 'advance_word');
  approx(gs ? gs.p50 : null, 150, 0.01, 'advance_word P50', failures);
  exactEq(result.tools.lessonVersions.length, 1, 'lesson_version 数', failures);
  exactEq(result.tools.lessonVersions[0] ? result.tools.lessonVersions[0].version : 0, 2, 'lesson version', failures);

  return { result, failures };
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.errors.length) {
    for (const e of args.errors) console.error('错误: ' + e);
    console.error('用 --help 查看用法');
    process.exit(1);
  }
  if (args.help) {
    console.log([
      '用法: node scripts/rt-timing-report.js <rt-timing-*.jsonl> [更多.jsonl ...] [--server-log <路径>]... [--json]',
      '      node scripts/rt-timing-report.js --self-test [--json]',
      '或:   npm run rt-report -- <同上>',
      '',
      '输入: 客户端 rt-timing-*.jsonl（TimingLog 导出）；可选 --server-log（可多次，zap JSON 行，自动抽取 rt_event）。',
      '输出: 控制台时序报告（首次问候 / clock_offset 校准句末→首可听 / 辅助同钟对照 / 响应与打断 P50,P95,失败计数 / 工具执行）。'
    ].join('\n'));
    process.exit(0);
  }

  if (args.selfTest) {
    const { result, failures } = selfTest();
    if (args.json) {
      console.log(JSON.stringify(result, null, 2));
    } else {
      console.log(render(result));
      console.log('');
    }
    if (failures.length) {
      for (const f of failures) console.error('SELF-TEST FAIL: ' + f);
      console.error('self-test: ' + failures.length + ' 项断言失败');
      process.exit(1);
    }
    console.log('self-test: 全部断言通过（含 clock_offset 会话分支、无 offset 会话分支、失败样本计数>0）');
    process.exit(0);
  }

  if (!args.files.length) {
    console.error('错误: 至少提供一个 rt-timing-*.jsonl 文件（或 --self-test）');
    process.exit(1);
  }
  const clientSessions = args.files.map(loadClientSession);
  const serverSessions = args.serverLogs.map(loadServerEvents);
  const result = analyze(clientSessions, serverSessions.length ? serverSessions : null);
  if (args.json) {
    console.log(JSON.stringify(result, null, 2));
  } else {
    console.log(render(result));
  }
}

if (require.main === module) {
  main();
}

module.exports = { analyze, render, selfTest, parseServerText, loadClientSession, loadServerEvents };
