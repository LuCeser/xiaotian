/**
 * @file Parent page — learning-record view (Phase 13.3, Plan 03).
 *
 * LessonsView: the 学习记录 tab body — the recent-lessons list
 * (GET /api/v1/lessons, summary rows) plus a per-lesson vertical detail
 * view (GET /api/v1/lessons/{id}). Mirrors the BrowseView component
 * shape: IIFE + window mount, container in / refresh() out, skeleton
 * rows while fetching, empty state, inline failure banner + 重试, and
 * the shared toast (window.__parentToast).
 *
 * Data contract (13.3-02 wire shape, snake_case — this view renders ONLY
 * what the server sends; in-progress lessons carry summary fields ONLY,
 * so the view structurally cannot show word surfaces for them):
 *   list row: { lesson_id, created_at, word_count, stage, kind,
 *               completed, unchecked, save_status,
 *               correct, wrong, unconfirmed, category }
 *   detail:   the summary row plus, for COMPLETED lessons:
 *               words:        [{word, meaning, example}]
 *               results:      [{word, correct, manual?, hints,
 *                               rating?, outcome?, ocr_text?}]
 *               save_receipts:[{lesson_id, request_id, kind, requested,
 *                               updated, missing, status,
 *                               created_at, completed_at}]
 *             (words/results/save_receipts are ABSENT for in-progress
 *             lessons — the summary IS the whole response; emptiness is
 *             data-driven, never shape-guessed.)
 *   category (Issue #10) is the row's subject ("english"/"chinese") —
 *   rendered as the SAME 英语/中文 badge the browse view uses, read from
 *   this field and NEVER parsed out of the lesson id. Absent (a pre-#10
 *   payload) renders no badge.
 *
 * Honest-copy contract (verbatim strings, ROADMAP 成功标准 3):
 *   save_status four states (server receipt truth — NEVER inferred from
 *   stage=submitted):
 *     saved   → 已保存
 *     failed  → 保存未完成
 *     pending → 待核实
 *     unknown → 保存状态未知 (+ the 历史记录 note on completed rows)
 *   judgement counts, completed rows only — unknown words are the
 *   server's unconfirmed remainder; the denominator is NEVER massaged
 *   and NO percentage appears anywhere:
 *     已核对 X 对 / Y 错 / Z 未确认
 *   kind badges: lesson → 到期复习, practice → 加练, unknown → 类型待核实
 *   in-progress stage summary: 默写中 (stage='') / 检查中 (checking) /
 *   进行中 (anything else unexpected)
 *   practice detail header, fixed line: 加练记录：不调整复习计划
 *   receipt kinds: check 检查提交 / correction 事后修正 /
 *   unchecked_fallback 未核对兜底 / practice 加练记录, each with its
 *   status copy and the real 保存 X / Y counts (missing rides along when
 *   non-empty).
 * The save-status area is READ-ONLY by design: the only write-shaped
 * control in this view is 刷新 (re-read the same data). No re-submit, no
 * re-judge, no re-open — 对账 belongs to Phase 15.
 *
 * Security (Pitfall 10 / T-13.3-07): ALL data rendering goes through
 * document.createElement + Node.textContent — the raw-markup setter that
 * Pitfall 10 bans is never used anywhere in this file. Containers clear
 * via the firstChild-removeChild loop.
 */

(function (global) {
  'use strict';

  var SKELETON_ROWS = 3;

  /** Verbatim four-state save copy (see file-head contract). */
  var SAVE_STATUS_COPY = {
    saved: '已保存',
    failed: '保存未完成',
    pending: '待核实',
    unknown: '保存状态未知'
  };

  /** Verbatim kind badge copy. Issue #12: poem = whole-poem 背默 attempts;
   * Issue #14: passage = passage-range 背默 attempts. */
  var KIND_COPY = {
    lesson: '到期复习',
    practice: '加练',
    poem: '古诗背默',
    passage: '课文背默',
    unknown: '类型待核实'
  };

  /** Subject badge copy (Issue #10) — the browse view's 英语/中文 pair. */
  var SUBJECT_COPY = {
    english: '英语',
    chinese: '中文'
  };

  /** passageRangeLabel — one passage attempt's practiced range as parent
   * copy (Issue #14): 整篇 or the original paragraph numbers. Same wording
   * discipline as the classroom's greeting/receipt. */
  function passageRangeLabel(item) {
    if (!item) return '？';
    if (item.passage_whole) return '整篇';
    var nos = item.passage_paragraphs || [];
    if (!nos.length) return '？';
    return '第 ' + nos.join('、') + ' 段';
  }

  /** Verbatim receipt-kind copy (the submission endpoints + Issue #12/#14). */
  var RECEIPT_KIND_COPY = {
    check: '检查提交',
    correction: '事后修正',
    unchecked_fallback: '未核对兜底',
    practice: '加练记录',
    poem: '古诗背默记录',
    passage: '课文背默记录'
  };

  /** In-progress stage summary; anything unmapped reads 进行中. */
  var STAGE_COPY = {
    '': '默写中',
    checking: '检查中'
  };

  /** outcome values (dictation.go outcomeConfirmed/outcomeTimeout). */
  var OUTCOME_COPY = {
    confirmed: '确认',
    timeout: '超时'
  };

  /** Clear a container via the firstChild-removeChild loop (Pattern 4). */
  function clearContainer(container) {
    while (container.firstChild) {
      container.removeChild(container.firstChild);
    }
  }

  /** Shared toast helper (parent-modes.js exposes __parentToast). */
  function toast(message, variant) {
    if (typeof global.__parentToast === 'function') {
      global.__parentToast(message, variant);
    }
  }

  function errorMessage(err) {
    return (err && err.message) ? err.message : '未知错误';
  }

  /** created_at "YYYY-MM-DD HH:MM:SS" → its date part, verbatim (the
      server stamps UTC; converting timezones here would mislabel days). */
  function formatDay(createdAt) {
    return (typeof createdAt === 'string' && createdAt.length >= 10)
      ? createdAt.slice(0, 10)
      : String(createdAt || '');
  }

  /** Unknown status values degrade to unknown — never invented states. */
  function saveStatusValue(status) {
    return Object.prototype.hasOwnProperty.call(SAVE_STATUS_COPY, status)
      ? status
      : 'unknown';
  }

  function stageLabel(item) {
    return Object.prototype.hasOwnProperty.call(STAGE_COPY, item.stage)
      ? STAGE_COPY[item.stage]
      : '进行中';
  }

  /**
   * options:
   *   fetchLessons()  → Promise<Array> — GET /api/v1/lessons (list rows)
   *   fetchLesson(id) → Promise<Object> — GET /api/v1/lessons/{id}
   */
  function LessonsView(container, options) {
    options = options || {};
    this.fetchLessons = options.fetchLessons || function () {
      return Promise.reject(new Error('LessonsView: missing fetchLessons handler'));
    };
    this.fetchLesson = options.fetchLesson || function () {
      return Promise.reject(new Error('LessonsView: missing fetchLesson handler'));
    };

    this.container = container;
    this.lessons = [];
    this.fetchSeq = 0;      // stale-response guard (list vs detail races)
    this.currentDetailId = null;

    this.listHost = document.createElement('div');
    this.listHost.className = 'lessons-list';
    this.listHost.setAttribute('aria-live', 'polite');
    this.container.appendChild(this.listHost);
  }

  /**
   * Re-fetch the lessons list and re-render. Also the 返回列表/重试
   * handler: the list is always re-read, never replayed from cache.
   */
  LessonsView.prototype.refresh = function () {
    var seq = ++this.fetchSeq;
    this.currentDetailId = null;
    this.renderSkeleton();
    var self = this;
    return this.fetchLessons().then(function (lessons) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.lessons = Array.isArray(lessons) ? lessons : [];
      self.renderList();
    }).catch(function (err) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.renderError(err);
    });
  };

  /** Skeleton shimmer rows while GET /api/v1/lessons is in flight. */
  LessonsView.prototype.renderSkeleton = function () {
    clearContainer(this.listHost);
    for (var i = 0; i < SKELETON_ROWS; i++) {
      var row = document.createElement('div');
      row.className = 'skeleton-row';
      row.setAttribute('aria-hidden', 'true');
      this.listHost.appendChild(row);
    }
  };

  /** Inline load-failure banner with 重试 (BrowseView precedent). */
  LessonsView.prototype.renderError = function (err) {
    clearContainer(this.listHost);
    var self = this;
    var banner = document.createElement('p');
    banner.className = 'list-error';
    banner.textContent = '加载失败：' + errorMessage(err) + '。点「重试」再试一次。';
    var retryBtn = document.createElement('button');
    retryBtn.type = 'button';
    retryBtn.className = 'btn btn-secondary';
    retryBtn.textContent = '重试';
    retryBtn.addEventListener('click', function () {
      self.refresh();
    });
    this.listHost.appendChild(banner);
    this.listHost.appendChild(retryBtn);
  };

  LessonsView.prototype.renderList = function () {
    clearContainer(this.listHost);
    if (!this.lessons.length) {
      this.renderEmptyState();
      return;
    }
    var self = this;
    this.lessons.forEach(function (item) {
      self.listHost.appendChild(self.buildListRow(item));
    });
  };

  /** Empty state (ROADMAP copy): no lessons yet → invite to start. */
  LessonsView.prototype.renderEmptyState = function () {
    var empty = document.createElement('p');
    empty.className = 'list-empty lessons-empty';
    empty.textContent = '还没有学习记录，开始今天的听写吧';
    this.listHost.appendChild(empty);
  };

  /**
   * Subject badge (Issue #10): the shared 英语/中文 pill, rendered from the
   * row's category field. null when the payload carries no known subject
   * (a pre-#10 summary) — the row then renders exactly as it did before.
   */
  LessonsView.prototype.buildSubjectBadge = function (category) {
    if (!Object.prototype.hasOwnProperty.call(SUBJECT_COPY, category)) {
      return null;
    }
    var badge = document.createElement('span');
    badge.className = 'badge badge-' + (category === 'chinese' ? 'chinese' : 'english');
    badge.setAttribute('data-coding', category);
    badge.textContent = SUBJECT_COPY[category];
    return badge;
  };

  /**
   * One list row (a full-row button): 日期 + kind badge + 学科徽章 + 词数,
   * then the honest payload — completed rows show the judgement counts
   * (and the 未核对 flag), in-progress rows show ONLY the stage summary.
   * The save chip renders on completed rows: a lesson still in the room
   * has nothing saved yet, and 「保存状态未知（历史记录）」 would be a lie
   * about it.
   */
  LessonsView.prototype.buildListRow = function (item) {
    var self = this;
    var row = document.createElement('button');
    row.type = 'button';
    row.className = 'lesson-row';
    row.setAttribute('data-lesson-id', item.lesson_id || '');

    var top = document.createElement('span');
    top.className = 'lesson-row-top';
    var day = document.createElement('span');
    day.className = 'lesson-day';
    day.textContent = formatDay(item.created_at);
    var kind = document.createElement('span');
    kind.className = 'lesson-kind' + (item.kind === 'practice' ? ' lesson-kind-practice' : '');
    kind.textContent = KIND_COPY[item.kind] || KIND_COPY.unknown;
    top.appendChild(day);
    top.appendChild(kind);
    var subject = this.buildSubjectBadge(item.category);
    if (subject) {
      top.appendChild(subject);
    }

    var mid = document.createElement('span');
    mid.className = 'lesson-row-mid';
    if (item.kind === 'poem') {
      // Issue #12: a poem row names the WORK (《题》·作者 · 背默) and counts
      // LINES — the whole-poem scope, never per-line word wording.
      var work = document.createElement('span');
      work.className = 'lesson-words';
      work.textContent = '《' + (item.poem_title || '古诗') + '》' +
        (item.poem_author ? ' · ' + item.poem_author : '') +
        ' · ' + (item.method || '背默');
      mid.appendChild(work);
    } else if (item.kind === 'passage') {
      // Issue #14: a passage row names the work AND the practiced range —
      // the paragraph attempt and the whole attempt stay distinguishable,
      // and the range always reads from the record row itself.
      var passageWork = document.createElement('span');
      passageWork.className = 'lesson-words';
      passageWork.textContent = '《' + (item.passage_title || '课文') + '》' +
        (item.passage_author ? ' · ' + item.passage_author : '') +
        ' · ' + passageRangeLabel(item) + ' · ' + (item.method || '背默');
      mid.appendChild(passageWork);
    } else {
      var words = document.createElement('span');
      words.className = 'lesson-words';
      words.textContent = (item.word_count || 0) + ' 词';
      mid.appendChild(words);
    }

    if (item.completed) {
      var counts = document.createElement('span');
      counts.className = 'lesson-counts';
      if (item.kind === 'poem') {
        counts.textContent = (item.word_count || 0) === (item.correct || 0)
          ? '整首 ' + (item.word_count || 0) + ' 句全对，已掌握'
          : '共 ' + (item.word_count || 0) + ' 句，对 ' + (item.correct || 0) + ' 句';
      } else if (item.kind === 'passage') {
        counts.textContent = (item.word_count || 0) === (item.correct || 0)
          ? passageRangeLabel(item) + ' ' + (item.word_count || 0) + ' 段全对，已掌握'
          : passageRangeLabel(item) + '共 ' + (item.word_count || 0) + ' 段，对 ' + (item.correct || 0) + ' 段';
      } else {
        counts.textContent = '已核对 ' + (item.correct || 0) + ' 对 / ' +
          (item.wrong || 0) + ' 错 / ' + (item.unconfirmed || 0) + ' 未确认';
      }
      mid.appendChild(counts);
      if (item.unchecked) {
        var flag = document.createElement('span');
        flag.className = 'lesson-flag';
        flag.textContent = '未核对';
        mid.appendChild(flag);
      }
    } else {
      var stage = document.createElement('span');
      stage.className = 'lesson-stage';
      stage.textContent = stageLabel(item);
      mid.appendChild(stage);
    }

    row.appendChild(top);
    row.appendChild(mid);
    if (item.completed) {
      var bottom = document.createElement('span');
      bottom.className = 'lesson-row-bottom';
      bottom.appendChild(this.buildSaveChip(item.save_status, true));
      row.appendChild(bottom);
    }

    row.addEventListener('click', function () {
      self.openDetail(item);
    });
    return row;
  };

  /**
   * Four-state save chip with the verbatim copy. Completed rows with an
   * unknown status carry the 历史记录 note (no receipt row exists — the
   * honest reading of a pre-receipt lesson).
   */
  LessonsView.prototype.buildSaveChip = function (status, withHistoryNote) {
    var value = saveStatusValue(status);
    var chip = document.createElement('span');
    chip.className = 'save-chip save-chip-' + value;
    chip.textContent = SAVE_STATUS_COPY[value];
    if (value === 'unknown' && withHistoryNote) {
      var note = document.createElement('span');
      note.className = 'save-chip-note';
      note.textContent = '历史记录';
      chip.appendChild(note);
    }
    return chip;
  };

  // ------------------------------------------------------------------
  // Detail view: 课次头 + 竖排逐词行 + 保存记录（回执逐条）. Entered by
  // tapping a list row; the only exits are 返回列表 and 刷新 (re-read).
  // ------------------------------------------------------------------

  LessonsView.prototype.openDetail = function (item) {
    var seq = ++this.fetchSeq;
    this.currentDetailId = item.lesson_id;
    this.renderDetailSkeleton(item);
    var self = this;
    this.fetchLesson(item.lesson_id).then(function (detail) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.renderDetail(detail || item);
    }).catch(function (err) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.renderDetailError(item, err);
    });
  };

  /** Skeleton while the detail fetch is in flight. */
  LessonsView.prototype.renderDetailSkeleton = function () {
    clearContainer(this.listHost);
    for (var i = 0; i < SKELETON_ROWS; i++) {
      var row = document.createElement('div');
      row.className = 'skeleton-row';
      row.setAttribute('aria-hidden', 'true');
      this.listHost.appendChild(row);
    }
  };

  /** Detail load failure: banner + 重试 (re-fetches the same lesson). */
  LessonsView.prototype.renderDetailError = function (item, err) {
    clearContainer(this.listHost);
    var self = this;
    var detail = document.createElement('div');
    detail.className = 'lesson-detail';

    var banner = document.createElement('p');
    banner.className = 'list-error';
    banner.textContent = '加载失败：' + errorMessage(err) + '。点「重试」再试一次。';
    var retryBtn = document.createElement('button');
    retryBtn.type = 'button';
    retryBtn.className = 'btn btn-secondary';
    retryBtn.textContent = '重试';
    retryBtn.addEventListener('click', function () {
      self.openDetail(item);
    });
    var backBtn = this.buildBackButton();
    detail.appendChild(banner);
    detail.appendChild(retryBtn);
    detail.appendChild(backBtn);
    this.listHost.appendChild(detail);
  };

  LessonsView.prototype.buildBackButton = function () {
    var self = this;
    var backBtn = document.createElement('button');
    backBtn.type = 'button';
    backBtn.className = 'btn btn-secondary lesson-back';
    backBtn.textContent = '返回列表';
    backBtn.addEventListener('click', function () {
      self.refresh();
    });
    return backBtn;
  };

  /**
   * Render one lesson's detail. The server decides the shape:
   *   - in-progress → the summary object (words/results/save_receipts
   *     keys absent) → header + progress note, nothing word-shaped.
   *   - completed   → header (counts + chips + practice note) + one
   *     vertical row per word + the receipt list.
   */
  LessonsView.prototype.renderDetail = function (detail) {
    clearContainer(this.listHost);
    var completed = !!detail.completed;
    var hasDetailKeys = Array.isArray(detail.results) || Array.isArray(detail.words);

    var wrap = document.createElement('div');
    wrap.className = 'lesson-detail';

    // Header: back/refresh actions, then 日期 + kind + honest status.
    var head = document.createElement('div');
    head.className = 'lesson-detail-head';

    var actions = document.createElement('div');
    actions.className = 'lesson-detail-actions';
    actions.appendChild(this.buildBackButton());
    var self = this;
    var refreshBtn = document.createElement('button');
    refreshBtn.type = 'button';
    refreshBtn.className = 'btn btn-secondary lesson-refresh';
    refreshBtn.textContent = '刷新';
    refreshBtn.addEventListener('click', function () {
      self.openDetail({ lesson_id: detail.lesson_id, completed: detail.completed });
    });
    actions.appendChild(refreshBtn);
    head.appendChild(actions);

    var meta = document.createElement('div');
    meta.className = 'lesson-detail-meta';
    var day = document.createElement('span');
    day.className = 'lesson-day';
    day.textContent = formatDay(detail.created_at);
    var kind = document.createElement('span');
    kind.className = 'lesson-kind' + (detail.kind === 'practice' ? ' lesson-kind-practice' : '');
    kind.textContent = KIND_COPY[detail.kind] || KIND_COPY.unknown;
    meta.appendChild(day);
    meta.appendChild(kind);
    var subject = this.buildSubjectBadge(detail.category);
    if (subject) {
      meta.appendChild(subject);
    }
    if (completed) {
      meta.appendChild(this.buildSaveChip(detail.save_status, true));
      if (detail.unchecked) {
        var flag = document.createElement('span');
        flag.className = 'lesson-flag';
        flag.textContent = '未核对';
        meta.appendChild(flag);
      }
    } else {
      var stage = document.createElement('span');
      stage.className = 'lesson-stage';
      stage.textContent = stageLabel(detail);
      meta.appendChild(stage);
    }
    head.appendChild(meta);

    if (detail.kind === 'practice') {
      // Fixed practice note (加练 never touches the FSRS schedule).
      var practiceNote = document.createElement('p');
      practiceNote.className = 'lesson-practice-note';
      practiceNote.textContent = '加练记录：不调整复习计划';
      head.appendChild(practiceNote);
    }
    if (detail.kind === 'poem') {
      // Issue #12: the poem attempt's own identity + scheduling contract —
      // whole-poem records never enter the word/FSRS plan (the 衔接方式 the
      // parent sees on the record itself).
      var poemNote = document.createElement('p');
      poemNote.className = 'lesson-practice-note';
      poemNote.textContent = '《' + (detail.poem_title || '古诗') + '》' +
        (detail.poem_author ? ' · ' + detail.poem_author : '') +
        ' · ' + (detail.method || '背默') + '：按整首记录掌握，不进入单词复习计划';
      head.appendChild(poemNote);
    }
    if (detail.kind === 'passage') {
      // Issue #14: the passage attempt's own identity + range — the record
      // names WHICH range was practiced, and the mastery verdict never
      // spreads beyond it (第 2、3 段掌握不算全文掌握).
      var passageNote = document.createElement('p');
      passageNote.className = 'lesson-practice-note';
      passageNote.textContent = '《' + (detail.passage_title || '课文') + '》' +
        (detail.passage_author ? ' · ' + detail.passage_author : '') +
        ' · ' + passageRangeLabel(detail) + ' · ' + (detail.method || '背默') +
        '：按本次范围记录掌握，不代表整篇课文';
      head.appendChild(passageNote);
    }

    if (completed) {
      var counts = document.createElement('p');
      counts.className = 'lesson-detail-counts';
      if (detail.kind === 'poem') {
        counts.textContent = (detail.word_count || 0) === (detail.correct || 0)
          ? '整首 ' + (detail.word_count || 0) + ' 句全对，已掌握'
          : '共 ' + (detail.word_count || 0) + ' 句，对 ' + (detail.correct || 0) + ' 句';
      } else if (detail.kind === 'passage') {
        counts.textContent = (detail.word_count || 0) === (detail.correct || 0)
          ? passageRangeLabel(detail) + ' ' + (detail.word_count || 0) + ' 段全对，已掌握'
          : passageRangeLabel(detail) + '共 ' + (detail.word_count || 0) + ' 段，对 ' + (detail.correct || 0) + ' 段';
      } else {
        counts.textContent = '已核对 ' + (detail.correct || 0) + ' 对 / ' +
          (detail.wrong || 0) + ' 错 / ' + (detail.unconfirmed || 0) + ' 未确认';
      }
      head.appendChild(counts);
    } else {
      var progressNote = document.createElement('p');
      progressNote.className = 'lesson-progress-note';
      progressNote.textContent = '这次课还没结束（' + stageLabel(detail) +
        '），结束后这里会显示逐词结果。';
      head.appendChild(progressNote);
    }
    wrap.appendChild(head);

    if (completed && hasDetailKeys) {
      wrap.appendChild(this.buildWordList(detail));
      wrap.appendChild(this.buildReceiptList(detail));
    }

    this.listHost.appendChild(wrap);
  };

  /**
   * The vertical per-word list — one stacked row per result: 词面 /
   * 释义 / 判定徽标 (manual=false → 未确认) / 提示 N 次 / outcome /
   * 识别：…. Meanings come from the words array (the lesson's ground
   * truth), matched by word; practice rows (no meanings stored) simply
   * render without one.
   */
  LessonsView.prototype.buildWordList = function (detail) {
    var meanings = {};
    (detail.words || []).forEach(function (w) {
      if (w && w.word) {
        meanings[w.word] = w.meaning || '';
      }
    });

    var list = document.createElement('div');
    list.className = 'lesson-words';
    var results = Array.isArray(detail.results) ? detail.results : [];
    var self = this;
    // Issue #14: a passage attempt's rows carry the ORIGINAL 自然段号 of the
    // practiced paragraphs (the range's own order) so the parent checks the
    // record against the text's own numbering.
    var paragraphNos = detail.kind === 'passage'
      ? (detail.passage_whole
        ? results.map(function (_, i) { return i + 1; })
        : (detail.passage_paragraphs || []))
      : null;
    results.forEach(function (r, i) {
      if (!r || !r.word) {
        return;
      }
      var row = self.buildWordRow(r, meanings[r.word] || '');
      var no = paragraphNos ? paragraphNos[i] : 0;
      if (no) {
        var label = document.createElement('span');
        label.className = 'lesson-paragraph-no';
        label.textContent = '第 ' + no + ' 段';
        row.insertBefore(label, row.firstChild);
      }
      list.appendChild(row);
    });
    return list;
  };

  LessonsView.prototype.buildWordRow = function (result, meaning) {
    var row = document.createElement('div');
    row.className = 'lesson-word-row';
    row.setAttribute('data-word', result.word);

    var line1 = document.createElement('div');
    line1.className = 'lesson-word-line';
    var face = document.createElement('span');
    face.className = 'lesson-word-face';
    face.textContent = result.word;
    line1.appendChild(face);

    var verdict = document.createElement('span');
    verdict.className = 'lesson-verdict';
    if (result.manual === false) {
      // Derived/OCR-prefilled judgement — NOT human-confirmed (完成勾≠写对).
      verdict.className += ' lesson-verdict-unconfirmed';
      verdict.textContent = '未确认';
    } else {
      verdict.className += result.correct ? ' lesson-verdict-ok' : ' lesson-verdict-wrong';
      verdict.textContent = result.correct ? '对' : '错';
    }
    line1.appendChild(verdict);
    row.appendChild(line1);

    if (meaning) {
      var meaningEl = document.createElement('div');
      meaningEl.className = 'lesson-word-meaning';
      meaningEl.textContent = meaning;
      row.appendChild(meaningEl);
    }

    var metaBits = [];
    if (result.hints > 0) {
      metaBits.push('提示 ' + result.hints + ' 次');
    }
    if (result.outcome) {
      metaBits.push(OUTCOME_COPY[result.outcome] || result.outcome);
    }
    if (result.ocr_text) {
      metaBits.push('识别：' + result.ocr_text);
    }
    if (metaBits.length) {
      var meta = document.createElement('div');
      meta.className = 'lesson-word-meta';
      meta.textContent = metaBits.join(' · ');
      row.appendChild(meta);
    }
    return row;
  };

  /**
   * 保存记录: one line per receipt row — kind in Chinese + status chip +
   * the real 保存 X / Y counts (missing rides along when non-empty).
   * Read-only: no retry/reconcile entries (Phase 15 territory).
   */
  LessonsView.prototype.buildReceiptList = function (detail) {
    var receipts = Array.isArray(detail.save_receipts) ? detail.save_receipts : [];

    var section = document.createElement('div');
    section.className = 'lesson-receipts';
    var title = document.createElement('h3');
    title.className = 'lesson-receipts-title';
    title.textContent = '保存记录';
    section.appendChild(title);

    if (!receipts.length) {
      var none = document.createElement('p');
      none.className = 'lesson-receipt lesson-receipt-empty';
      none.textContent = '没有保存记录';
      section.appendChild(none);
      return section;
    }

    var self = this;
    receipts.forEach(function (rc) {
      var line = document.createElement('p');
      line.className = 'lesson-receipt';

      var kind = document.createElement('span');
      kind.className = 'lesson-receipt-kind';
      kind.textContent = RECEIPT_KIND_COPY[rc.kind] || rc.kind;
      line.appendChild(kind);

      line.appendChild(self.buildSaveChip(rc && rc.status, false));

      var requested = Array.isArray(rc.requested) ? rc.requested.length : 0;
      var updated = Array.isArray(rc.updated) ? rc.updated.length : 0;
      var missing = Array.isArray(rc.missing) ? rc.missing.length : 0;
      var counts = document.createElement('span');
      counts.className = 'lesson-receipt-counts';
      counts.textContent = '保存 ' + updated + ' / ' + requested +
        (missing > 0 ? '，缺 ' + missing + ' 个' : '');
      line.appendChild(counts);

      section.appendChild(line);
    });
    return section;
  };

  // Window-or-module-global tail (parent-browse.js convention) so Node
  // tests can require this file later.
  global.LessonsView = LessonsView;
})(typeof window !== 'undefined' ? window : this);
