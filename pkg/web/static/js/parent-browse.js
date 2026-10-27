/**
 * @file Parent page — browse view (Phase 9, Plan 07).
 *
 * BrowseView: the 浏览词库 card body — a toolbar (search over word+meaning
 * with a ✕ clear button, category pills 全部/英语/中文), collapsible unit
 * groups with word rows, and the edit modal. This rebuilds the Plan 09-01
 * tracer WordList internally (flat list → grouped browse view); the
 * component boundary (container in, refresh() out) is unchanged.
 *
 * Contracts (UI-SPEC + plan 09-07 must-haves):
 *   - Search debounces 300ms (vanilla setTimeout/clearTimeout) then calls
 *     GET /api/v1/words?q=; clearing restores the unfiltered list.
 *   - Unit header: 48px aria-expanded toggle (chevron ▾/▸ + unit name +
 *     count badge {n} 个单词 from the rows in that group); collapse state
 *     persists in localStorage under parent.units.collapsed (JSON array
 *     of unit names, re-applied on every render). GET /api/v1/units counts
 *     the legacy unit tag, which lesson-owned words can still wear while
 *     the page shows them under their lesson (Issue #89).
 *   - Groups are built client-side. A word with textbook lesson ownership
 *     groups by that lesson within its OWN textbook (Issue #89 — the same
 *     record dictation reads; two books' same-named lessons are separate
 *     groups), never by the legacy unit tag. A word with no textbook
 *     assignment still groups by its unit string (补充词汇). Groups left
 *     with zero rows by the search are hidden, not rendered empty.
 *   - Edit modal (编辑单词): only 单词 is required (D-08); category is
 *     read-only (D-07); 保存修改 → PUT /api/v1/words/{id} → toast +
 *     refresh.
 *   - States: 3 skeleton shimmer rows while GET /api/v1/words is in
 *     flight; zero-words empty copy; no-match copy; inline load-failure
 *     banner with 重试.
 *
 * Security (Pitfall 10 / T-09-01): ALL data rendering goes through
 * document.createElement + Node.textContent — the raw-markup setter that
 * Pitfall 10 bans is never used anywhere in this file. Containers
 * clear via the firstChild-removeChild loop (Shared Pattern 4).
 */

(function (global) {
  'use strict';

  var DEBOUNCE_MS = 300;
  var COLLAPSE_STORAGE_KEY = 'parent.units.collapsed';
  var SKELETON_ROWS = 3;

  /** Header label for supplementary rows whose unit is the empty string. */
  var UNGROUPED_LABEL = '未分组';
  /** Header label for textbook rows whose lesson is still empty. */
  var PENDING_LESSON_LABEL = '待确认';

  /**
   * lessonOwnership — the textbook lesson the browse group must use, as
   * {bookId, bookName, lesson}, or null when the word has no textbook
   * assignment and still groups by its unit tag. The lesson NAME alone
   * cannot key a group: two textbooks can both carry 「第1课」 and their
   * lessons are independent (课次属于教材), so the owning book rides
   * along. A non-supplementary scope_reason wins (including an empty
   * lesson, which is 待确认 — one shared bucket). Without a verdict, the
   * first named assignment on the word is the group so a just-assigned
   * lesson still moves the row.
   */
  function lessonOwnership(word) {
    if (!word) {
      return null;
    }
    var entries = word.textbooks || [];
    var reason = word.scope_reason || '';
    var lesson = '';
    if (reason && reason !== 'supplementary') {
      lesson = word.group_lesson || '';
    } else if (entries.length) {
      for (var i = 0; i < entries.length; i++) {
        if (entries[i] && entries[i].lesson) {
          lesson = entries[i].lesson;
          break;
        }
      }
    } else {
      return null;
    }
    return owningAssignment(word, lesson);
  }

  /**
   * owningAssignment — the assignment a lesson group keys by: the entry
   * the server verdict named (group_lesson plus its group_textbook_id),
   * else the first entry wearing that lesson. When the attribution
   * projection is absent the wire's bare book id still keys the group
   * (0 = unknown). An empty lesson is 待确认 and never claims a book.
   */
  function owningAssignment(word, lesson) {
    if (!lesson) {
      return { bookId: 0, bookName: '', lesson: '' };
    }
    var entries = word.textbooks || [];
    var bookId = word.group_textbook_id || 0;
    var wearing = null;
    for (var i = 0; i < entries.length; i++) {
      var entry = entries[i];
      if (!entry || entry.lesson !== lesson) {
        continue;
      }
      if (!wearing) {
        wearing = entry;
      }
      if (bookId && entry.id === bookId) {
        wearing = entry;
        break;
      }
    }
    if (wearing) {
      return { bookId: wearing.id || 0, bookName: wearing.name || '', lesson: lesson };
    }
    return { bookId: bookId, bookName: '', lesson: lesson };
  }

  /**
   * dictationStatusText — the parent-facing line when this word will not
   * be picked for dictation, or '' when the textbook gate and the
   * participation switch both admit it. A removed word says so FIRST:
   * eligibility is participates AND 课次门槛, so 「学到后自动加入」 would
   * be a false promise while participates is off — learning the lesson
   * alone would not bring the word back.
   */
  function dictationStatusText(word) {
    if (!word) {
      return '';
    }
    if (word.participates === false) {
      return '已移出默写范围';
    }
    if (word.scope_reason === 'unlearned') {
      return '尚未学到，学到后自动加入';
    }
    if (word.scope_reason === 'off_list') {
      return '课次不在教材课次表里，不会随进度自动加入';
    }
    if (word.scope_reason === 'pending') {
      return '课次待确认，指定后自动加入';
    }
    return '';
  }

  /**
   * wordNeedsScopeRecover — a textbook-owned word whose lesson is still
   * 待确认 and has not been manually included yet (Issue #80 AC-08). The
   * row action calls assign with includeInScope without opening the bulk
   * modal.
   */
  function wordNeedsScopeRecover(word) {
    if (!word || word.participates === false) {
      return null;
    }
    if (word.scope_reason !== 'pending') {
      return null;
    }
    var hit = null;
    (word.textbooks || []).forEach(function (entry) {
      if (!entry || entry.lesson || entry.includeInScope) {
        return;
      }
      if (!hit || entry.id === hit.id) {
        hit = entry;
      }
    });
    return hit;
  }

  /**
   * groupBrowseWords — one group per lesson (words that have textbook
   * ownership) or per unit tag (supplementary words). A lesson group is
   * keyed by its BOOK and lesson name together: two books' 「第1课」 are
   * independent lessons, never one merged group. Order is first-seen;
   * orderBrowseGroups re-sorts lesson groups into teaching order.
   */
  function groupBrowseWords(words) {
    var groups = [];
    var index = Object.create(null);
    (words || []).forEach(function (word) {
      var own = lessonOwnership(word);
      var key;
      if (own !== null) {
        key = 'lesson\u0000' + own.bookId + '\u0000' + own.lesson;
        if (!Object.prototype.hasOwnProperty.call(index, key)) {
          index[key] = {
            kind: 'lesson',
            unit: own.lesson,
            bookId: own.bookId,
            bookName: own.bookName,
            label: own.lesson || PENDING_LESSON_LABEL,
            rows: [],
          };
          groups.push(index[key]);
        }
      } else {
        var unit = (word && word.unit) || '';
        key = 'unit\u0000' + unit;
        if (!Object.prototype.hasOwnProperty.call(index, key)) {
          index[key] = {
            kind: 'unit',
            unit: unit,
            label: unit || UNGROUPED_LABEL,
            rows: [],
          };
          groups.push(index[key]);
        }
      }
      index[key].rows.push(word);
    });
    return groups;
  }

  /**
   * orderBrowseGroups — lesson groups follow their OWN textbook's explicit
   * lesson order: books in list order, each book's lessons in list order
   * (not dictionary order, not the legacy unit sort, and never one global
   * name order that would interleave two books' independent 「第1课」).
   * 待确认 follows the named lessons. Supplementary unit groups keep
   * first-seen order at the end.
   */
  function orderBrowseGroups(groups, books) {
    var rank = Object.create(null);
    var bookName = Object.create(null);
    var lessonName = Object.create(null);
    var n = 0;
    (books || []).forEach(function (book) {
      if (!book) {
        return;
      }
      bookName[String(book.id)] = book.name || '';
      (book.lessons || []).forEach(function (name) {
        if (!name) {
          return;
        }
        lessonName[name] = true;
        var key = String(book.id) + '\u0000' + name;
        if (!Object.prototype.hasOwnProperty.call(rank, key)) {
          rank[key] = n;
          n += 1;
        }
      });
    });
    var named = [];
    var pending = [];
    var units = [];
    (groups || []).forEach(function (group) {
      if (!group || group.kind !== 'lesson') {
        units.push(group);
        return;
      }
      if (!group.unit) {
        pending.push(group);
        return;
      }
      named.push(group);
    });
    named.sort(function (a, b) {
      var ka = String(a.bookId || 0) + '\u0000' + a.unit;
      var kb = String(b.bookId || 0) + '\u0000' + b.unit;
      var ra = Object.prototype.hasOwnProperty.call(rank, ka) ? rank[ka] : 100000;
      var rb = Object.prototype.hasOwnProperty.call(rank, kb) ? rank[kb] : 100000;
      return ra - rb;
    });
    // A lesson name worn by TWO books names two independent groups — both
    // carry their book so the headers cannot read as one lesson.
    var byLesson = Object.create(null);
    named.forEach(function (group) {
      if (!group.unit) {
        return;
      }
      if (!Object.prototype.hasOwnProperty.call(byLesson, group.unit)) {
        byLesson[group.unit] = [];
      }
      byLesson[group.unit].push(group);
    });
    Object.keys(byLesson).forEach(function (lesson) {
      var wearers = byLesson[lesson];
      if (wearers.length < 2) {
        return;
      }
      wearers.forEach(function (group) {
        var name = group.bookName || bookName[String(group.bookId)] || '';
        if (name) {
          group.label = name + '·' + lesson;
        }
      });
    });
    // A supplementary unit tag that shares a name with a textbook lesson
    // is a different bucket. Prefix it so two headers do not both read
    // 「unit 1」 (a lesson group rendered below also claims the name, even
    // when the books list itself failed to load).
    Object.keys(byLesson).forEach(function (lesson) {
      lessonName[lesson] = true;
    });
    units.forEach(function (group) {
      if (!group || !group.unit) {
        return;
      }
      if (lessonName[group.unit]) {
        group.label = '补充 · ' + group.unit;
      }
    });
    return named.concat(pending, units);
  }

  var DELETE_WORDS_BATCH = 100;

  /**
   * supplementaryDeletePlan — DELETE bodies for one supplementary group.
   * Only the group's own rows are named. A {category, unit} delete would
   * also remove lesson-owned cards that still carry the same unit string
   * and are shown under their lesson (Issue #89). Words mode is capped at
   * 100 per request, so a long group is split.
   */
  function supplementaryDeletePlan(group) {
    var buckets = Object.create(null);
    var order = [];
    var count = 0;
    var rows = (group && group.rows) || [];
    rows.forEach(function (word) {
      if (!word || !word.category || !word.word) {
        return;
      }
      if (!Object.prototype.hasOwnProperty.call(buckets, word.category)) {
        buckets[word.category] = [];
        order.push(word.category);
      }
      buckets[word.category].push(word.word);
      count += 1;
    });
    var requests = [];
    order.forEach(function (category) {
      var words = buckets[category];
      for (var i = 0; i < words.length; i += DELETE_WORDS_BATCH) {
        requests.push({
          category: category,
          words: words.slice(i, i + DELETE_WORDS_BATCH),
        });
      }
    });
    return { count: count, requests: requests };
  }

  /**
   * unitRenameTouchesLesson — true when any loaded card wears this unit
   * string but groups by a textbook lesson. Renaming the unit tag would
   * rewrite that hidden string; the group's own rows must be edited
   * instead.
   */
  function unitRenameTouchesLesson(words, unit) {
    for (var i = 0; i < (words || []).length; i++) {
      var word = words[i];
      if (!word || (word.unit || '') !== unit) {
        continue;
      }
      if (lessonOwnership(word) !== null) {
        return true;
      }
    }
    return false;
  }

  /** groupHoldText — the hold line when every row shares one, else ''. */
  function groupHoldText(rows) {
    var text = '';
    for (var i = 0; i < (rows || []).length; i++) {
      var line = dictationStatusText(rows[i]);
      if (!line) {
        return '';
      }
      if (!text) {
        text = line;
      } else if (text !== line) {
        return '';
      }
    }
    return text;
  }

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
    } else if (global.console && typeof global.console.warn === 'function') {
      // No toast host registered: log so the feedback is not silently lost
      // (no new UI element is introduced here).
      global.console.warn('[parent-browse] toast(' + (variant || 'info') + '): ' + message);
    }
  }

  function errorMessage(err) {
    return (err && err.message) ? err.message : '未知错误';
  }

  // Participation failures carry the script's English stdout diagnostic on
  // 404 ("card ids not found: ...") — fine for logs, wrong for the family
  // UI (AGENTS.md 语言约定), so map the known code to parent-facing copy.
  function participationErrorMessage(err) {
    if (err && err.code === 'not_found') {
      return '部分词条已不存在，本次没有保存';
    }
    return errorMessage(err);
  }

  // ------------------------------------------------------------------
  // Collapse state: localStorage parent.units.collapsed — a JSON array
  // of collapsed unit names. Every read is defensive: private-mode
  // storage or corrupt JSON degrades to "everything expanded" without
  // breaking the browse view.
  // ------------------------------------------------------------------

  function readCollapsedNames() {
    try {
      var raw = global.localStorage.getItem(COLLAPSE_STORAGE_KEY);
      var parsed = raw ? JSON.parse(raw) : [];
      if (!Array.isArray(parsed)) {
        return [];
      }
      return parsed.filter(function (name) {
        return typeof name === 'string';
      });
    } catch (e) {
      return [];
    }
  }

  function writeCollapsedNames(names) {
    try {
      global.localStorage.setItem(COLLAPSE_STORAGE_KEY, JSON.stringify(names));
    } catch (e) {
      // Storage unavailable — collapse still works, it just does not
      // survive a reload.
    }
  }

  // ------------------------------------------------------------------
  // Loading button (same contract as parent-modes.js: disabled + inline
  // spinner, label kept). Local copy keeps the module independent.
  // ------------------------------------------------------------------

  function buildLoadingButton(labelText, className) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = className || 'btn btn-primary';
    var spinner = document.createElement('span');
    spinner.className = 'btn-spinner';
    spinner.setAttribute('aria-hidden', 'true');
    var label = document.createElement('span');
    label.className = 'btn-label';
    label.textContent = labelText;
    btn.appendChild(spinner);
    btn.appendChild(label);
    return btn;
  }

  function setLoading(btn, loading) {
    btn.disabled = loading;
    btn.classList.toggle('loading', loading);
  }

  // ------------------------------------------------------------------
  // Modal helper: role=dialog + aria-modal, labelled by its title,
  // focus-trapped (Tab cycles inside), Esc closes, overlay
  // rgba(0,0,0,0.4), max-width 480px. Clicking the overlay is inert —
  // destructive confirms must not be dismissible by a stray tap.
  // ------------------------------------------------------------------

  var modalSeq = 0;

  /**
   * Open a modal. spec:
   *   title — heading text (the dialog is labelled by it)
   *   danger — true applies the destructive emphasis styling
   *   build(body, actions, close) — append fields/messages to body and
   *     the action buttons (取消 first) to actions; call close() to
   *     dismiss. Form modals move the actions div into their <form> so
   *     Enter submits.
   * On close the previously focused element regains focus.
   */
  function openModal(spec) {
    modalSeq += 1;
    var titleId = 'modal-title-' + modalSeq;

    var overlay = document.createElement('div');
    overlay.className = 'modal-overlay';

    var dialog = document.createElement('div');
    dialog.className = 'modal' + (spec.danger ? ' modal-danger' : '') + (spec.wide ? ' modal-wide' : '');
    dialog.setAttribute('role', 'dialog');
    dialog.setAttribute('aria-modal', 'true');
    dialog.setAttribute('aria-labelledby', titleId);

    var title = document.createElement('h3');
    title.className = 'modal-title';
    title.id = titleId;
    title.textContent = spec.title;

    var body = document.createElement('div');
    body.className = 'modal-body';
    var actions = document.createElement('div');
    actions.className = 'modal-actions';

    dialog.appendChild(title);
    dialog.appendChild(body);
    dialog.appendChild(actions);
    overlay.appendChild(dialog);
    document.body.appendChild(overlay);

    var previouslyFocused = document.activeElement;
    var closed = false;

    function close() {
      if (closed) {
        return;
      }
      closed = true;
      document.removeEventListener('keydown', onKeydown, true);
      if (overlay.parentNode) {
        overlay.parentNode.removeChild(overlay);
      }
      if (previouslyFocused && typeof previouslyFocused.focus === 'function') {
        previouslyFocused.focus();
      }
    }

    function focusables() {
      var nodes = dialog.querySelectorAll('button, input, textarea, select, a[href]');
      var list = [];
      for (var i = 0; i < nodes.length; i++) {
        if (!nodes[i].disabled && nodes[i].tabIndex >= 0) {
          list.push(nodes[i]);
        }
      }
      return list;
    }

    function onKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        close();
        return;
      }
      if (event.key !== 'Tab') {
        return;
      }
      var list = focusables();
      if (!list.length) {
        return;
      }
      var first = list[0];
      var last = list[list.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener('keydown', onKeydown, true);
    spec.build(body, actions, close);

    // Initial focus: first input of a form modal; for confirm dialogs
    // (no input) this is the first action button = 取消, so a rushed
    // Enter cannot confirm a destructive action.
    var firstInput = dialog.querySelector('input, textarea');
    if (firstInput && !firstInput.disabled) {
      firstInput.focus();
    } else {
      var buttons = focusables();
      if (buttons.length) {
        buttons[0].focus();
      }
    }
  }

  /** Build one labeled modal field. Returns {field, input, error}. */
  function buildModalField(labelText, id, isTextarea) {
    var field = document.createElement('div');
    field.className = 'form-field';
    var label = document.createElement('label');
    label.className = 'form-label';
    label.setAttribute('for', id);
    label.textContent = labelText;
    var input;
    if (isTextarea) {
      input = document.createElement('textarea');
      input.rows = 3;
      input.className = 'form-textarea';
    } else {
      input = document.createElement('input');
      input.type = 'text';
      input.className = 'form-input';
    }
    input.id = id;
    input.name = id;
    input.setAttribute('autocomplete', 'off');
    var error = document.createElement('p');
    error.className = 'form-error';
    error.setAttribute('aria-live', 'polite');
    field.appendChild(label);
    field.appendChild(input);
    field.appendChild(error);
    return { field: field, input: input, error: error };
  }

  // ------------------------------------------------------------------
  // BrowseView — toolbar + unit groups + word rows + edit modal
  // ------------------------------------------------------------------

  /**
   * options:
   *   fetchWords(category, q)  → Promise<Array> — GET /api/v1/words
   *   fetchUnits(category)     → Promise<Array> — GET /api/v1/units
   *       (count badge source; all-categories merges by name — 09-04)
   *   editWord(id, payload)    → Promise — PUT /api/v1/words/{id}
   *   setParticipation(ids, enabled) → Promise — PUT /api/v1/words/
   *       participation with {ids: [...], participates} (one request per
   *       batch; the server caps one batch at 500 unique ids)
   *   renameUnit(old, neu, category) → Promise — POST /api/v1/units/rename
   *       (wired in Plan 09-07 Task 2's rename modal)
   *   deleteWords(payload)     → Promise — DELETE /api/v1/words
   *       (wired in Plan 09-07 Task 2's destructive confirms)
   *   onMutated()              — fired after a successful mutation so the
   *       host can refresh shared state (unit datalist)
   *   onUnitSuggestions(category) — the edit modal's 单元 input shares the
   *       ONE datalist; scope suggestions to the edited word's category
   */
  function BrowseView(container, options) {
    options = options || {};
    this.fetchWords = options.fetchWords || function () {
      return Promise.reject(new Error('BrowseView: missing fetchWords handler'));
    };
    this.fetchUnits = options.fetchUnits || function () {
      return Promise.resolve([]);
    };
    this.editWord = options.editWord || function () {
      return Promise.reject(new Error('BrowseView: missing editWord handler'));
    };
    this.setParticipation = options.setParticipation || function () {
      return Promise.reject(new Error('BrowseView: missing setParticipation handler'));
    };
    this.renameUnit = options.renameUnit || function () {
      return Promise.reject(new Error('BrowseView: missing renameUnit handler'));
    };
    this.deleteWords = options.deleteWords || function () {
      return Promise.reject(new Error('BrowseView: missing deleteWords handler'));
    };
    this.onMutated = options.onMutated || function () {};
    this.onUnitSuggestions = options.onUnitSuggestions || function () {};
    // Issue #4 hooks (parent-textbook.js surfaces): open the 教材管理
    // modal from the toolbar and the 批量指定 modal from the bulk bar.
    this.onManageTextbooks = options.onManageTextbooks || null;
    this.onAssignTextbook = options.onAssignTextbook || null;
    this.assignWords = options.assignWords || null;
    // Issue #11: the unified content view's 古诗 page — the whole-poem
    // list, the explicit attribution write and the textbook list the
    // assign modal reads. Absent fetchPoems ⇒ the 类型 filter never
    // mounts and the view stays exactly the pre-#11 词语 page.
    this.fetchPoems = options.fetchPoems || null;
    this.assignPoem = options.assignPoem || null;
    this.fetchTextbooks = options.fetchTextbooks || null;
    // Issue #13: the unified content view's 课文 page — the
    // structured-passage list, the practice-range selection writes
    // (add/remove) and the explicit attribution write. Absent
    // fetchPassages ⇒ the 课文 pill never mounts.
    this.fetchPassages = options.fetchPassages || null;
    this.assignPassage = options.assignPassage || null;
    this.addPassageScope = options.addPassageScope || null;
    this.removePassageScope = options.removePassageScope || null;

    this.container = container;
    this.category = '';   // '' = 全部 | 'english' | 'chinese'
    this.type = 'words';  // 'words' | 'poems' | 'passages' (default 词语)
    this.query = '';
    this.words = [];
    this.poems = [];
    this.passages = [];
    this.units = [];
    this.textbooks = [];
    this.fetchSeq = 0;    // stale-response guard for debounced refetches
    this.debounceTimer = null;
    this.selectedIds = {};
    this.bulkInFlight = false; // one batch write at a time; keeps buttons disabled

    this.buildToolbar();
  }

  /** Build the toolbar once; only the list region re-renders. */
  BrowseView.prototype.buildToolbar = function () {
    var self = this;

    var toolbar = document.createElement('div');
    toolbar.className = 'browse-toolbar';

    var searchBox = document.createElement('div');
    searchBox.className = 'search-box';

    this.searchInput = document.createElement('input');
    this.searchInput.type = 'text';
    this.searchInput.className = 'search-input';
    this.searchInput.id = 'browse-search';
    this.searchInput.placeholder = '搜索单词或意思';
    this.searchInput.setAttribute('autocomplete', 'off');
    this.searchInput.setAttribute('aria-label', '搜索单词或意思');

    this.clearBtn = document.createElement('button');
    this.clearBtn.type = 'button';
    this.clearBtn.className = 'search-clear';
    this.clearBtn.setAttribute('aria-label', '清空搜索');
    this.clearBtn.textContent = '✕';
    this.clearBtn.hidden = true;

    searchBox.appendChild(this.searchInput);
    searchBox.appendChild(this.clearBtn);

    var pills = document.createElement('div');
    pills.className = 'category-pills';
    pills.setAttribute('role', 'group');
    pills.setAttribute('aria-label', '分类筛选');
    this.pillButtons = [];
    [
      { value: '', text: '全部' },
      { value: 'english', text: '英语' },
      { value: 'chinese', text: '中文' },
    ].forEach(function (pill) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'pill' + (pill.value === '' ? ' active' : '');
      btn.textContent = pill.text;
      btn.setAttribute('aria-pressed', pill.value === '' ? 'true' : 'false');
      btn.addEventListener('click', function () {
        self.setCategory(pill.value);
      });
      self.pillButtons.push({ value: pill.value, btn: btn });
      pills.appendChild(btn);
    });

    toolbar.appendChild(searchBox);
    toolbar.appendChild(pills);

    // Issue #11/#13: the unified content view's 类型 filter — 词语 (the
    // existing page) | 古诗 | 课文. The poems pill mounts only when the
    // poem list handler is wired; the passages pill only when the passage
    // list handler is; otherwise the toolbar stays exactly the pre-#11
    // shape.
    if (typeof this.fetchPoems === 'function' || typeof this.fetchPassages === 'function') {
      var typePills = document.createElement('div');
      typePills.className = 'category-pills type-pills';
      typePills.setAttribute('role', 'group');
      typePills.setAttribute('aria-label', '类型筛选');
      this.typePillButtons = [];
      [
        { value: 'words', text: '词语', wired: true },
        { value: 'poems', text: '古诗', wired: typeof this.fetchPoems === 'function' },
        { value: 'passages', text: '课文', wired: typeof this.fetchPassages === 'function' },
      ].forEach(function (pill) {
        if (!pill.wired) {
          return;
        }
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'pill' + (pill.value === 'words' ? ' active' : '');
        btn.textContent = pill.text;
        btn.setAttribute('aria-pressed', pill.value === 'words' ? 'true' : 'false');
        btn.addEventListener('click', function () {
          self.setType(pill.value);
        });
        self.typePillButtons.push({ value: pill.value, btn: btn });
        typePills.appendChild(btn);
      });
      toolbar.appendChild(typePills);
    }

    // Issue #4: the 教材管理 entry rides the toolbar (textbook entities,
    // lesson order and learned progress all live behind it).
    if (typeof this.onManageTextbooks === 'function') {
      var textbookBtn = document.createElement('button');
      textbookBtn.type = 'button';
      textbookBtn.className = 'btn btn-secondary textbook-manage-btn';
      textbookBtn.id = 'textbook-manage-btn';
      textbookBtn.textContent = '教材管理';
      textbookBtn.addEventListener('click', function () {
        self.onManageTextbooks(self.category);
      });
      toolbar.appendChild(textbookBtn);
    }

    var bulk = document.createElement('div');
    bulk.className = 'participation-bulk';
    this.bulkBar = bulk;
    this.selectionCount = document.createElement('span');
    this.selectionCount.className = 'selection-count';
    this.selectionCount.textContent = '未选择';
    this.bulkEnableBtn = document.createElement('button');
    this.bulkEnableBtn.type = 'button';
    this.bulkEnableBtn.className = 'btn btn-secondary';
    this.bulkEnableBtn.textContent = '加入默写范围';
    this.bulkDisableBtn = document.createElement('button');
    this.bulkDisableBtn.type = 'button';
    this.bulkDisableBtn.className = 'btn btn-secondary';
    this.bulkDisableBtn.textContent = '移出默写范围';
    this.bulkEnableBtn.disabled = true;
    this.bulkDisableBtn.disabled = true;
    this.bulkEnableBtn.addEventListener('click', function () { self.applyBulkParticipation(true); });
    this.bulkDisableBtn.addEventListener('click', function () { self.applyBulkParticipation(false); });
    bulk.appendChild(this.selectionCount);
    bulk.appendChild(this.bulkEnableBtn);
    bulk.appendChild(this.bulkDisableBtn);

    // Issue #4: batch textbook/lesson attribution for the selection.
    if (typeof this.onAssignTextbook === 'function') {
      this.bulkAssignBtn = document.createElement('button');
      this.bulkAssignBtn.type = 'button';
      this.bulkAssignBtn.className = 'btn btn-secondary';
      this.bulkAssignBtn.id = 'bulk-assign-textbook-btn';
      this.bulkAssignBtn.textContent = '指定教材课次';
      this.bulkAssignBtn.disabled = true;
      this.bulkAssignBtn.addEventListener('click', function () {
        var ids = self.selectedIds;
        var idList = Object.keys(ids).map(function (k) { return parseInt(k, 10); });
        var words = [];
        (self.words || []).forEach(function (w) {
          if (ids[String(w.id)]) {
            words.push(w);
          }
        });
        self.onAssignTextbook(idList, words);
      });
      bulk.appendChild(this.bulkAssignBtn);
    }

    // Issue #80 AC-09: 批量编辑 — paste the corrected word list over the
    // checked rows; the preview (parent-batch-edit.js) blocks identity
    // conflicts BEFORE any write, and the confirm rides one PUT per word.
    this.bulkEditBtn = document.createElement('button');
    this.bulkEditBtn.type = 'button';
    this.bulkEditBtn.className = 'btn btn-secondary';
    this.bulkEditBtn.id = 'bulk-edit-btn';
    this.bulkEditBtn.textContent = '批量编辑';
    this.bulkEditBtn.disabled = true;
    this.bulkEditBtn.addEventListener('click', function () {
      self.openBatchEditModal();
    });
    bulk.appendChild(this.bulkEditBtn);

    // The browse list region announces polite updates (UI-SPEC
    // Accessibility Contract).
    this.listHost = document.createElement('div');
    this.listHost.className = 'browse-list';
    this.listHost.setAttribute('aria-live', 'polite');

    this.container.appendChild(toolbar);
    this.container.appendChild(bulk);
    this.container.appendChild(this.listHost);

    // Search: debounced 300ms → re-fetch with q (vanilla setTimeout/
    // clearTimeout guard — no debounce analog in PATTERNS).
    this.searchInput.addEventListener('input', function () {
      self.query = self.searchInput.value.trim();
      self.updateClearVisibility();
      if (self.debounceTimer) {
        clearTimeout(self.debounceTimer);
      }
      self.debounceTimer = setTimeout(function () {
        self.debounceTimer = null;
        self.refresh();
      }, DEBOUNCE_MS);
    });

    // ✕ clears the query, cancels any pending debounce, restores the
    // unfiltered list.
    this.clearBtn.addEventListener('click', function () {
      if (self.debounceTimer) {
        clearTimeout(self.debounceTimer);
        self.debounceTimer = null;
      }
      self.searchInput.value = '';
      self.query = '';
      self.updateClearVisibility();
      self.refresh();
      self.searchInput.focus();
    });
  };

  BrowseView.prototype.updateSelectionControls = function () {
    var count = Object.keys(this.selectedIds).length;
    if (count > 500) {
      // The server rejects batches beyond 500 ids — surface the cap instead
      // of letting the doomed request fail after the fact.
      this.selectionCount.textContent = '已选择 ' + count + ' 项（一次最多 500 项）';
    } else {
      this.selectionCount.textContent = count ? ('已选择 ' + count + ' 项') : '未选择';
    }
    var actionable = count > 0 && count <= 500 && !this.bulkInFlight;
    this.bulkEnableBtn.disabled = !actionable;
    this.bulkDisableBtn.disabled = !actionable;
    if (this.bulkAssignBtn) {
      this.bulkAssignBtn.disabled = !actionable;
    }
    if (this.bulkEditBtn) {
      // Issue #80 AC-09: the batch word/meaning edit rides the 词语 list
      // only (poems/passages are edited as whole works).
      this.bulkEditBtn.disabled = !actionable || this.type !== 'words';
    }
  };

  BrowseView.prototype.clearSelection = function () {
    this.selectedIds = {};
    this.updateSelectionControls();
  };

  BrowseView.prototype.applyBulkParticipation = function (enabled) {
    if (this.bulkInFlight) { return; }
    var ids = Object.keys(this.selectedIds);
    if (!ids.length || ids.length > 500) { return; }
    var self = this;
    this.bulkInFlight = true;
    this.updateSelectionControls();
    this.setParticipation(ids.map(Number), enabled).then(function () {
      self.selectedIds = {};
      toast(enabled ? '已加入默写范围' : '已移出默写范围', 'success');
      return self.refresh();
    }).catch(function (err) {
      toast((enabled ? '加入默写范围失败：' : '移出默写范围失败：') + participationErrorMessage(err) + '。已重新读取当前状态。', 'error');
      return self.refresh();
    }).then(function () {
      self.bulkInFlight = false;
      self.updateSelectionControls();
    });
  };

  BrowseView.prototype.updateClearVisibility = function () {
    this.clearBtn.hidden = this.searchInput.value === '';
  };

  BrowseView.prototype.setCategory = function (value) {
    if (this.category === value) {
      return;
    }
    this.category = value;
    this.pillButtons.forEach(function (pill) {
      var active = pill.value === value;
      pill.btn.classList.toggle('active', active);
      pill.btn.setAttribute('aria-pressed', active ? 'true' : 'false');
    });
    this.refresh();
  };

  /**
   * setType (Issue #11/#13) — switch the unified content view between the
   * 词语, 古诗 and 课文 pages. The bulk participation bar is a WORD
   * surface only (poems and passages never enter the dictation
   * selection), and the search placeholder follows the page so the q
   * contract stays honest.
   */
  BrowseView.prototype.setType = function (value) {
    if (this.type === value) {
      return;
    }
    this.type = value;
    (this.typePillButtons || []).forEach(function (pill) {
      var active = pill.value === value;
      pill.btn.classList.toggle('active', active);
      pill.btn.setAttribute('aria-pressed', active ? 'true' : 'false');
    });
    this.bulkBar.hidden = value !== 'words';
    this.searchInput.placeholder = value === 'poems'
      ? '搜索诗题、作者或诗句'
      : (value === 'passages' ? '搜索课文标题或正文' : '搜索单词或意思');
    this.selectedIds = {};
    this.refresh();
  };

  /**
   * Re-fetch the current type's list and re-render. The sequence token
   * drops stale responses when a debounced search races a pill switch.
   */
  BrowseView.prototype.refresh = function () {
    var seq = ++this.fetchSeq;
    this.renderSkeleton();
    var self = this;
    if (this.type === 'poems') {
      return this.fetchPoems(this.category, this.query).then(function (poems) {
        if (seq !== self.fetchSeq) {
          return;
        }
        self.poems = Array.isArray(poems) ? poems : [];
        self.render();
      }).catch(function (err) {
        if (seq !== self.fetchSeq) {
          return;
        }
        self.renderError(err);
      });
    }
    if (this.type === 'passages') {
      return this.fetchPassages(this.category, this.query).then(function (passages) {
        if (seq !== self.fetchSeq) {
          return;
        }
        self.passages = Array.isArray(passages) ? passages : [];
        self.render();
      }).catch(function (err) {
        if (seq !== self.fetchSeq) {
          return;
        }
        self.renderError(err);
      });
    }
    var booksPromise = typeof this.fetchTextbooks === 'function'
      ? this.fetchTextbooks(this.category).catch(function () { return []; })
      : Promise.resolve([]);
    return Promise.all([
      this.fetchWords(this.category, this.query),
      this.fetchUnits(this.category),
      booksPromise,
    ]).then(function (results) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.words = Array.isArray(results[0]) ? results[0] : [];
      self.units = Array.isArray(results[1]) ? results[1] : [];
      self.textbooks = Array.isArray(results[2]) ? results[2] : [];
      var visible = {};
      self.words.forEach(function (word) { visible[String(word.id)] = true; });
      Object.keys(self.selectedIds).forEach(function (id) {
        if (!visible[id]) { delete self.selectedIds[id]; }
      });
      self.updateSelectionControls();
      self.render();
    }).catch(function (err) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.renderError(err);
    });
  };

  /** 3 skeleton shimmer rows while GET /api/v1/words is in flight. */
  BrowseView.prototype.renderSkeleton = function () {
    clearContainer(this.listHost);
    for (var i = 0; i < SKELETON_ROWS; i++) {
      var row = document.createElement('div');
      row.className = 'skeleton-row';
      row.setAttribute('aria-hidden', 'true');
      this.listHost.appendChild(row);
    }
  };

  /** Inline load-failure banner with 重试 (UI-SPEC error/list-load row). */
  BrowseView.prototype.renderError = function (err) {
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

  BrowseView.prototype.render = function () {
    clearContainer(this.listHost);

    // Issue #11: the 古诗 page renders whole works — no selection, no
    // bulk bar, no dictation surfaces (poems are not cards).
    if (this.type === 'poems') {
      if (!this.poems.length) {
        this.renderEmptyState();
        return;
      }
      var self = this;
      var note = document.createElement('p');
      note.className = 'list-note';
      note.textContent = '古诗按整首保存与练习；学习范围随教材已学进度或手动加入生效，进入范围后可在卡片上开始整首背默。';
      this.listHost.appendChild(note);
      this.poems.forEach(function (poem) {
        self.listHost.appendChild(self.buildPoemCard(poem));
      });
      return;
    }

    // Issue #13: the 课文 page renders whole originals with their
    // practice ranges — no selection, no bulk bar, no dictation surfaces
    // (passages are not cards; the passage classroom is a later task).
    if (this.type === 'passages') {
      if (!this.passages.length) {
        this.renderEmptyState();
        return;
      }
      var selfP = this;
      var passageNote = document.createElement('p');
      passageNote.className = 'list-note';
      passageNote.textContent = '课文按整篇保存，练习范围可选整篇或指定自然段、随时调整而不用重新导入；每个范围可单独背默，掌握与复习安排按范围分别记录，互不代替。';
      this.listHost.appendChild(passageNote);
      this.passages.forEach(function (passage) {
        selfP.listHost.appendChild(selfP.buildPassageCard(passage));
      });
      return;
    }

    if (!this.words.length) {
      this.renderEmptyState();
      return;
    }

    var groups = this.groupWords();
    var self2 = this;
    groups.forEach(function (group) {
      self2.listHost.appendChild(self2.buildGroup(group));
    });
  };

  /**
   * Empty states (UI Considerations empty/library + empty/search):
   * zero words → 词库还是空的 + guidance copy; no matches → 没有找到
   * 「{q}」相关的单词 + 换个关键词试试. Issue #11: the 古诗 page carries
   * its own pair (still empty / no poem matched).
   */
  BrowseView.prototype.renderEmptyState = function () {
    var title = document.createElement('p');
    title.className = 'list-empty list-empty-title';
    var hint = document.createElement('p');
    hint.className = 'list-empty list-empty-hint';

    if (this.type === 'poems') {
      if (this.query) {
        title.textContent = '没有找到「' + this.query + '」相关的古诗';
        hint.textContent = '换个关键词试试，或清空搜索看全部。';
      } else {
        title.textContent = '还没有古诗';
        hint.textContent = '在「照片导入」里拍一张带古诗的语文页面，核对后整首保存。';
      }
      this.listHost.appendChild(title);
      this.listHost.appendChild(hint);
      return;
    }

    if (this.type === 'passages') {
      if (this.query) {
        title.textContent = '没有找到「' + this.query + '」相关的课文';
        hint.textContent = '换个关键词试试，或清空搜索看全部。';
      } else {
        title.textContent = '还没有课文';
        hint.textContent = '在「照片导入」里拍一张课文页面，核对后整篇保存并选择练习范围。';
      }
      this.listHost.appendChild(title);
      this.listHost.appendChild(hint);
      return;
    }

    if (this.query) {
      title.textContent = '没有找到「' + this.query + '」相关的单词';
      hint.textContent = '换个关键词试试，或清空搜索看全部。';
    } else {
      title.textContent = '词库还是空的';
      hint.textContent = '从孩子的课本抄一课开始：点「批量添加」，一行一个词，粘贴进来就行。';
    }

    this.listHost.appendChild(title);
    this.listHost.appendChild(hint);
  };

  /**
   * Group the word list client-side. Textbook-owned words group by the
   * lesson dictation reads (group_lesson); supplementary words group by
   * the unit tag. Lesson groups then follow textbook teaching order.
   */
  BrowseView.prototype.groupWords = function () {
    return orderBrowseGroups(groupBrowseWords(this.words), this.textbooks);
  };

  /**
   * Unit rename/delete follow the rows in the group. A search hides some
   * of those rows, so the header actions are withheld until the list is
   * unfiltered — the confirm copy would otherwise name a different set
   * than the one on screen (T-09-05). Word rows keep their per-word
   * 编辑/删除 everywhere.
   */
  BrowseView.prototype.unitOpsAllowed = function () {
    return this.query === '';
  };

  BrowseView.prototype.buildGroup = function (group) {
    var self = this;
    var unit = group.unit;
    // A lesson group's collapse key carries its book: two books' 「第1课」
    // are independent groups and must not collapse together.
    var collapseKey = group.kind === 'lesson'
      ? ('课次:' + (group.bookId || 0) + ':' + unit)
      : unit;

    var wrap = document.createElement('div');
    wrap.className = 'unit-group';
    wrap.setAttribute('data-group-kind', group.kind || 'unit');
    wrap.setAttribute('data-unit', unit);

    var header = document.createElement('div');
    header.className = 'unit-header';

    // 48px header toggle button with aria-expanded (UI-SPEC Unit group
    // row). The header actions stay SIBLING buttons — nested buttons are
    // invalid HTML and break the accessibility tree.
    var toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'unit-toggle';

    var chevron = document.createElement('span');
    chevron.className = 'unit-chevron';
    chevron.setAttribute('aria-hidden', 'true');

    var name = document.createElement('span');
    name.className = 'unit-name';
    name.textContent = group.label || (unit || UNGROUPED_LABEL);
    // UI-SPEC long-text row: the full name stays available beyond the
    // single-line ellipsis (hover/tap-hold tooltip; assistive tech reads
    // the untruncated textContent regardless).
    if (unit) {
      name.title = group.label || unit;
    }

    // Count badge is the rows in this group. GET /api/v1/units counts the
    // legacy unit tag, which lesson-owned words can still wear while the
    // page shows them under their lesson.
    var badge = document.createElement('span');
    badge.className = 'unit-count';
    var deletePlan = group.kind === 'lesson' ? null : supplementaryDeletePlan(group);
    var count = deletePlan ? deletePlan.count : group.rows.length;
    badge.textContent = count + ' 个单词';

    toggle.appendChild(chevron);
    toggle.appendChild(name);
    toggle.appendChild(badge);
    header.appendChild(toggle);

    // Inline header actions (UI-SPEC Unit group row): 重命名 + 删除 —
    // 44px hit areas via padding. Only supplementary named units have a
    // {category, unit} target. A lesson group is textbook ownership;
    // renaming it here would edit the legacy tag and leave the lesson.
    if (group.kind !== 'lesson' && unit !== '' && this.unitOpsAllowed()) {
      var renameBtn = document.createElement('button');
      renameBtn.type = 'button';
      renameBtn.className = 'unit-action';
      renameBtn.textContent = '重命名';
      renameBtn.addEventListener('click', function () {
        self.openRenameModal(group);
      });

      var deleteBtn = document.createElement('button');
      deleteBtn.type = 'button';
      deleteBtn.className = 'unit-action unit-action-danger';
      deleteBtn.textContent = '删除';
      deleteBtn.addEventListener('click', function () {
        self.openDeleteUnitModal(group);
      });

      header.appendChild(renameBtn);
      header.appendChild(deleteBtn);
    }

    var body = document.createElement('div');
    body.className = 'unit-body';
    group.rows.forEach(function (word) {
      body.appendChild(self.buildRow(word));
    });

    function applyCollapse(names) {
      var isCollapsed = names.indexOf(collapseKey) !== -1;
      body.hidden = isCollapsed;
      chevron.textContent = isCollapsed ? '▸' : '▾';
      toggle.setAttribute('aria-expanded', isCollapsed ? 'false' : 'true');
    }
    applyCollapse(readCollapsedNames());

    toggle.addEventListener('click', function () {
      var names = readCollapsedNames();
      var idx = names.indexOf(collapseKey);
      if (idx === -1) {
        names.push(collapseKey);
      } else {
        names.splice(idx, 1);
      }
      writeCollapsedNames(names);
      applyCollapse(names);
    });

    wrap.appendChild(header);
    var hold = groupHoldText(group.rows);
    if (hold) {
      var holdLine = document.createElement('p');
      holdLine.className = 'unit-hold';
      holdLine.textContent = hold;
      wrap.appendChild(holdLine);
    }
    wrap.appendChild(body);
    return wrap;
  };

  /**
   * Word row: word 600 weight + meaning muted + category badge +
   * 编辑 action. 48px rows, 8px rhythm. data-word keeps the e2e
   * tracer selector contract from Plan 09-01.
   */
  BrowseView.prototype.buildRow = function (word) {
    var self = this;
    var row = document.createElement('div');
    row.className = 'word-row';
    row.setAttribute('data-word', word.word);
    row.setAttribute('data-id', String(word.id));

    // Issue #18: the 44px touch floor rides a wrapping label
    // (.inline-check-hit) — the input itself stays 18px (Chrome zeroes
    // used padding on native checkboxes, so the input cannot carry the
    // hit area itself).
    var selectHit = document.createElement('label');
    selectHit.className = 'inline-check-hit';
    var select = document.createElement('input');
    select.type = 'checkbox';
    select.className = 'word-select';
    select.setAttribute('aria-label', '选择 ' + word.word);
    select.checked = !!this.selectedIds[String(word.id)];
    select.addEventListener('change', function () {
      if (select.checked) { self.selectedIds[String(word.id)] = true; }
      else { delete self.selectedIds[String(word.id)]; }
      self.updateSelectionControls();
    });
    selectHit.appendChild(select);

    var text = document.createElement('span');
    text.className = 'word-text';
    text.textContent = word.word;

    var meaning = document.createElement('span');
    meaning.className = 'word-meaning';
    meaning.textContent = word.meaning || '';

    // Issue #8 AC-2 (后续查看保留来源区别): the 系统 badge marks a meaning
    // the import supplemented from the confirmed textbook context — the
    // same display rule as the preview (non-empty + generated). A SIBLING
    // of the meaning span so .word-meaning stays pure text. Edits in the
    // modal never flip the source (ADR-0005: a corrected supplement stays
    // a supplement, never masquerading as textbook original).
    var srcBadge = null;
    if (word.meaning && word.meaning_source === 'generated') {
      srcBadge = document.createElement('span');
      srcBadge.className = 'word-src-badge';
      srcBadge.textContent = '系统';
      srcBadge.title = '释义由系统按教材语境补充，家长可能已修改；编辑不会当作教材原文';
    }

    var badge = document.createElement('span');
    badge.className = 'badge badge-' + (word.category === 'chinese' ? 'chinese' : 'english');
    badge.setAttribute('data-coding', word.category);
    badge.textContent = word.category === 'chinese' ? '中文' : '英语';

    // Issue #4 attribution chips: 教材名·课次 / 教材名·待确认 per
    // assignment (parent-textbook.js's assignLabel; guarded so browse
    // still renders if that script is absent). Collected here, appended
    // after the category badge below.
    var chips = [];
    (word.textbooks || []).forEach(function (entry) {
      var chip = document.createElement('span');
      chip.className = 'textbook-chip' + (entry.lesson ? '' : ' textbook-chip-pending');
      chip.textContent = typeof global.assignLabel === 'function'
        ? global.assignLabel(entry)
        : (entry.name + (entry.lesson ? '·' + entry.lesson : '·待确认'));
      chip.title = entry.lesson
        ? '教材课次归属：' + entry.name
        : '课次待确认：暂不自动加入默写范围，确认课次或手动加入后生效';
      chips.push(chip);
    });

    var actions = document.createElement('span');
    actions.className = 'row-actions';

    var recover = wordNeedsScopeRecover(word);
    if (recover && typeof self.assignWords === 'function') {
      var recoverBtn = document.createElement('button');
      recoverBtn.type = 'button';
      recoverBtn.className = 'row-action row-action-scope';
      recoverBtn.textContent = '加入默写范围';
      recoverBtn.title = '课次待确认：不受进度限制，直接加入默写范围';
      recoverBtn.addEventListener('click', function () {
        recoverBtn.disabled = true;
        self.assignWords(recover.id, {
          cardIds: [word.id],
          lesson: '',
          includeInScope: true,
        }).then(function () {
          toast('已加入默写范围', 'success');
          return self.refresh();
        }).catch(function (err) {
          toast('加入默写范围失败：' + errorMessage(err) + '。', 'error');
          return self.refresh();
        });
      });
      actions.appendChild(recoverBtn);
    }

    var editBtn = document.createElement('button');
    editBtn.type = 'button';
    editBtn.className = 'row-action';
    editBtn.textContent = '编辑';
    editBtn.addEventListener('click', function () {
      self.openEditModal(word);
    });

    var deleteBtn = document.createElement('button');
    deleteBtn.type = 'button';
    deleteBtn.className = 'row-action row-action-danger';
    deleteBtn.textContent = '删除';
    deleteBtn.addEventListener('click', function () {
      self.openDeleteWordModal(word);
    });

    actions.appendChild(editBtn);
    actions.appendChild(deleteBtn);

    var participationLabel = document.createElement('label');
    participationLabel.className = 'participation-toggle';
    var participation = document.createElement('input');
    participation.type = 'checkbox';
    participation.checked = word.participates !== false;
    participation.setAttribute('aria-label', '加入默写范围 ' + word.word);
    var participationText = document.createElement('span');
    participationText.textContent = '加入默写范围';
    participation.addEventListener('change', function () {
      var wanted = participation.checked;
      participation.disabled = true;
      self.setParticipation([word.id], wanted).then(function () {
        toast(wanted ? '已加入默写范围' : '已移出默写范围', 'success');
      }).catch(function (err) {
        toast((wanted ? '加入默写范围失败：' : '移出默写范围失败：') + participationErrorMessage(err) + '。已重新读取当前状态。', 'error');
      }).then(function () {
        self.refresh();
      });
    });
    participationLabel.appendChild(participation);
    participationLabel.appendChild(participationText);

    row.appendChild(selectHit);
    row.appendChild(text);
    if (word.meaning) {
      row.appendChild(meaning);
    }
    if (srcBadge) {
      row.appendChild(srcBadge);
    }
    row.appendChild(badge);
    chips.forEach(function (chip) {
      row.appendChild(chip);
    });
    var scopeText = dictationStatusText(word);
    if (scopeText) {
      var scope = document.createElement('span');
      scope.className = 'word-scope-status';
      scope.setAttribute('data-scope', word.scope_reason || '');
      scope.textContent = scopeText;
      row.appendChild(scope);
    }
    row.appendChild(participationLabel);
    row.appendChild(actions);
    return row;
  };

  // ------------------------------------------------------------------
  // Issue #11 — the unified content view's 古诗 page. A poem renders as
  // ONE whole work (title, author, ordered lines) with its attribution
  // chips and the scope verdict — but NONE of the word dictation
  // surfaces: no selection checkbox, no 加入默写范围 toggle, no bulk
  // bar. Poems are not cards; the poem classroom is the NEXT task.
  // ------------------------------------------------------------------

  /** Parent-facing label for one poem's scope_reason (management state
   *  only — the wording never claims 可完成背默). */
  function poemScopeLabel(reason, inScope) {
    if (reason === 'included') {
      return '已加入学习范围（手动）';
    }
    if (reason === 'learned') {
      return '已学课次内，随进度在范围内';
    }
    if (reason === 'progress_pending') {
      return '课次尚未学到，暂不在学习范围';
    }
    // supplementary (or anything unknown): no attribution at all.
    return inScope ? '无教材归属（补充内容）' : '无教材归属';
  }

  /**
   * passageScopeLabel — the passage twin of poemScopeLabel: the SAME
   * Issue #4 verdict strings ride both content types, so one shared
   * mapping keeps the copy identical (delegates instead of duplicating).
   */
  function passageScopeLabel(reason, inScope) {
    return poemScopeLabel(reason, inScope);
  }

  /**
   * passageScopeText — one stored range as parent copy: 整篇 or the
   * explicit paragraph numbers (指定自然段).
   */
  function passageScopeText(scope) {
    if (!scope) {
      return '？';
    }
    return scope.whole ? '整篇' : ('第 ' + (scope.paragraphs || []).join('、') + ' 段');
  }

  /**
   * passageScopeReciteCopy — the per-range 背默 entry's copy (Issue #14):
   * the parent picks WHICH range this session covers; each stored range is
   * an independent practice target.
   */
  function passageScopeReciteCopy(scope) {
    return scope.whole ? '背默整篇' : ('背默' + passageScopeText(scope));
  }

  /**
   * passageScopePracticeLine — one range's OWN practice state (Issue #14):
   * mastered/not plus the persisted next-review day. Per range by design —
   * 第 2、3 段's state never reads as the whole passage's.
   */
  function passageScopePracticeLine(scope) {
    if (!scope || !scope.has_practice) {
      return '';
    }
    return scope.last_mastered
      ? '上次已掌握 · ' + scope.next_review_day + ' 再巩固'
      : '上次尚未掌握 · ' + scope.next_review_day + ' 再练';
  }

  /**
   * buildPoemCard — one whole poem: title + author header, the ordered
   * verse lines as one block (分行和顺序是作品本身的结构), the category
   * badge, attribution chips and the scope verdict. The single action is
   * 管理归属 (POST /poems/{id}/assign) — mounted only when both the
   * assign write and the textbook list are wired.
   */
  BrowseView.prototype.buildPoemCard = function (poem) {
    var self = this;
    var card = document.createElement('div');
    card.className = 'poem-card';
    card.setAttribute('data-poem-id', String(poem.id));
    card.setAttribute('data-poem-title', poem.title || '');

    var head = document.createElement('div');
    head.className = 'poem-head';

    var title = document.createElement('span');
    title.className = 'poem-title';
    title.textContent = poem.title || '（无诗题）';
    head.appendChild(title);

    if (poem.author) {
      var author = document.createElement('span');
      author.className = 'poem-author';
      author.textContent = poem.author;
      head.appendChild(author);
    }

    var badge = document.createElement('span');
    badge.className = 'badge badge-' + (poem.category === 'chinese' ? 'chinese' : 'english');
    badge.setAttribute('data-coding', poem.category);
    badge.textContent = poem.category === 'chinese' ? '中文' : '英语';
    head.appendChild(badge);

    var scope = document.createElement('span');
    scope.className = 'poem-scope poem-scope-' +
      (poem.scope_reason || 'supplementary') +
      (poem.in_scope ? ' poem-scope-in' : '');
    scope.textContent = poemScopeLabel(poem.scope_reason, poem.in_scope);
    head.appendChild(scope);

    if (this.assignPoem && this.fetchTextbooks) {
      var manageBtn = document.createElement('button');
      manageBtn.type = 'button';
      manageBtn.className = 'row-action poem-manage-btn';
      manageBtn.textContent = '管理归属';
      manageBtn.addEventListener('click', function () {
        self.openAssignPoemModal(poem);
      });
      head.appendChild(manageBtn);
    }

    card.appendChild(head);

    // The ordered verse lines: one <p> per line keeps the work's own
    // 分行和顺序 (never a joined blob, never re-orderable here).
    var lines = document.createElement('div');
    lines.className = 'poem-lines';
    (poem.lines || []).forEach(function (line) {
      var p = document.createElement('p');
      p.className = 'poem-line';
      p.textContent = line;
      lines.appendChild(p);
    });
    card.appendChild(lines);

    // Issue #12: the whole-poem 背默 entry. Only an IN-SCOPE poem can start
    // a classroom (从符合范围的整首古诗开始练习) — an out-of-scope card shows
    // the honest disabled reason instead. The door opens the student tutor
    // page with the poem id; handing the iPad to the child is the parent's
    // one gesture.
    var reciteRow = document.createElement('div');
    reciteRow.className = 'poem-recite-row';
    var reciteBtn = document.createElement('button');
    reciteBtn.type = 'button';
    reciteBtn.className = 'row-action poem-recite-btn';
    if (poem.in_scope) {
      reciteBtn.textContent = '背默这首';
      reciteBtn.title = '打开学生页，凭记忆默写整首';
      reciteBtn.addEventListener('click', function () {
        global.open('/tutor/?poem=' + encodeURIComponent(poem.id) +
          '&category=chinese', '_blank');
      });
    } else {
      reciteBtn.textContent = '暂不在学习范围';
      reciteBtn.disabled = true;
      reciteBtn.title = '课次尚未学到且未手动加入，暂不能开始背默';
    }
    reciteRow.appendChild(reciteBtn);
    card.appendChild(reciteRow);

    if (poem.has_practice && poem.next_review_day) {
      var reviewPlan = document.createElement('p');
      reviewPlan.className = 'poem-review-plan';
      reviewPlan.textContent = poem.last_mastered
        ? '上次整首已掌握 · ' + poem.next_review_day + ' 再巩固'
        : '上次尚未整首掌握 · ' + poem.next_review_day + ' 再练';
      card.appendChild(reviewPlan);
    }

    // Attribution chips — the word row's chip shape, plus a 手动 marker
    // on assignments the parent explicitly included.
    (poem.textbooks || []).forEach(function (entry) {
      var chip = document.createElement('span');
      chip.className = 'textbook-chip' + (entry.lesson ? '' : ' textbook-chip-pending');
      chip.textContent = (entry.name || '？') +
        (entry.lesson ? ('·' + entry.lesson) : '·待确认') +
        (entry.includeInScope ? '（手动加入）' : '');
      chip.title = entry.includeInScope
        ? '已手动加入学习范围：无论进度如何都在范围内'
        : (entry.lesson
          ? '教材课次归属：' + (entry.name || '') + '（随已学进度进入范围）'
          : '课次待确认：暂不进入学习范围，确认课次或手动加入后生效');
      card.appendChild(chip);
    });

    return card;
  };

  /**
   * openAssignPoemModal — the 古诗 page's explicit attribution management
   * (归属与进度管理): pick a textbook (the poem's OWN subject's books),
   * set the lesson and the explicit include together — POST
   * /poems/{id}/assign carries the whole desired state (the endpoint's
   * contract). A book-less subject or a failed list degrades honestly:
   * the modal says so instead of guessing.
   */
  BrowseView.prototype.openAssignPoemModal = function (poem) {
    var self = this;

    openModal({
      title: '管理古诗归属',
      build: function (body, actions, close) {
        var form = document.createElement('form');
        form.className = 'modal-form';
        form.noValidate = true;

        var poemLine = document.createElement('p');
        poemLine.className = 'modal-message';
        poemLine.textContent = '「' + (poem.title || '（无诗题）') + '」' +
          (poem.author ? '（' + poem.author + '）' : '');
        form.appendChild(poemLine);

        var bookField = document.createElement('div');
        var bookLabel = document.createElement('label');
        bookLabel.className = 'form-label';
        bookLabel.textContent = '教材';
        var bookSelect = document.createElement('select');
        bookSelect.className = 'form-input';
        bookSelect.setAttribute('aria-label', '教材');
        var loadingOption = document.createElement('option');
        loadingOption.textContent = '读取教材中…';
        bookSelect.appendChild(loadingOption);
        bookField.appendChild(bookLabel);
        bookField.appendChild(bookSelect);
        form.appendChild(bookField);

        var lessonField = buildModalField('课次', 'poem-lesson-input', false);
        lessonField.input.value = (poem.textbooks && poem.textbooks.length)
          ? (poem.textbooks[0].lesson || '') : '';
        form.appendChild(lessonField.field);

        var includeField = document.createElement('div');
        includeField.className = 'form-field';
        var includeLabel = document.createElement('label');
        includeLabel.className = 'participation-toggle';
        var include = document.createElement('input');
        include.type = 'checkbox';
        include.checked = !!(poem.textbooks && poem.textbooks[0] &&
          poem.textbooks[0].includeInScope);
        var includeText = document.createElement('span');
        includeText.textContent = '手动加入学习范围（不受已学进度限制）';
        includeLabel.appendChild(include);
        includeLabel.appendChild(includeText);
        includeField.appendChild(includeLabel);
        form.appendChild(includeField);

        var error = document.createElement('p');
        error.className = 'form-error';
        error.setAttribute('aria-live', 'polite');
        form.appendChild(error);

        // The textbook list rides the poem's OWN subject; a failed load
        // keeps the modal open with its reason (never a fabricated
        // empty list).
        self.fetchTextbooks(poem.category || '').then(function (books) {
          clearContainer(bookSelect);
          (books || []).forEach(function (book) {
            var opt = document.createElement('option');
            opt.value = String(book.id);
            opt.textContent = book.name;
            bookSelect.appendChild(opt);
          });
          if (!books || !books.length) {
            var none = document.createElement('option');
            none.value = '';
            none.textContent = '（该学科还没有教材）';
            bookSelect.appendChild(none);
            bookSelect.disabled = true;
            error.textContent = '该学科还没有教材，请先在「教材管理」中建立教材。';
            return;
          }
          // Preselect the poem's current attribution when present.
          if (poem.textbooks && poem.textbooks.length) {
            bookSelect.value = String(poem.textbooks[0].id);
          }
        }).catch(function (err) {
          clearContainer(bookSelect);
          var failed = document.createElement('option');
          failed.value = '';
          failed.textContent = '（教材读取失败）';
          bookSelect.appendChild(failed);
          bookSelect.disabled = true;
          error.textContent = '教材读取失败：' + errorMessage(err);
        });

        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);

        var saveBtn = buildLoadingButton('保存归属', 'btn btn-primary');
        saveBtn.type = 'submit';

        actions.appendChild(cancelBtn);
        actions.appendChild(saveBtn);
        form.appendChild(actions);
        body.appendChild(form);

        function handleSave() {
          if (saveBtn.disabled) {
            return;
          }
          error.textContent = '';
          var bookId = parseInt(bookSelect.value, 10);
          if (!bookId) {
            error.textContent = '请选择教材（或先在「教材管理」中建立教材）';
            return;
          }
          setLoading(saveBtn, true);
          self.assignPoem(poem.id, {
            textbook_id: bookId,
            lesson: lessonField.input.value.trim(),
            includeInScope: include.checked,
          }).then(function () {
            close();
            toast('已保存古诗归属', 'success');
            self.refresh();
            self.onMutated();
          }).catch(function (err) {
            setLoading(saveBtn, false);
            toast('保存失败：' + errorMessage(err) + '。内容没有保存，请检查后重试。', 'error');
          });
        }

        saveBtn.addEventListener('click', function (event) {
          event.preventDefault();
          handleSave();
        });
        form.addEventListener('submit', function (event) {
          event.preventDefault();
          handleSave();
        });
      },
    });
  };

  /**
   * buildPassageCard (Issue #13) — one whole passage: title + author
   * header, the ordered 自然段 (每段一行, boundaries and order are the
   * work's own structure; paragraphs inside a chosen range read marked),
   * the practice ranges with their own add/remove management (范围可查看、
   * 修改和重新选择 — the text itself never re-imports), the category
   * badge, attribution chips and the scope verdict.
   */
  BrowseView.prototype.buildPassageCard = function (passage) {
    var self = this;
    var card = document.createElement('div');
    card.className = 'passage-card';
    card.setAttribute('data-passage-id', String(passage.id));
    card.setAttribute('data-passage-title', passage.title || '');

    var head = document.createElement('div');
    head.className = 'passage-head';

    var title = document.createElement('span');
    title.className = 'passage-title';
    title.textContent = passage.title || '（无标题）';
    head.appendChild(title);

    if (passage.author) {
      var author = document.createElement('span');
      author.className = 'passage-author';
      author.textContent = passage.author;
      head.appendChild(author);
    }

    var badge = document.createElement('span');
    badge.className = 'badge badge-' + (passage.category === 'chinese' ? 'chinese' : 'english');
    badge.setAttribute('data-coding', passage.category);
    badge.textContent = passage.category === 'chinese' ? '中文' : '英语';
    head.appendChild(badge);

    var scope = document.createElement('span');
    scope.className = 'passage-scope passage-scope-' +
      (passage.scope_reason || 'supplementary') +
      (passage.in_scope ? ' passage-scope-in' : '');
    scope.textContent = passageScopeLabel(passage.scope_reason, passage.in_scope);
    head.appendChild(scope);

    if (this.assignPassage && this.fetchTextbooks) {
      var manageBtn = document.createElement('button');
      manageBtn.type = 'button';
      manageBtn.className = 'row-action passage-manage-btn';
      manageBtn.textContent = '管理归属';
      manageBtn.addEventListener('click', function () {
        self.openAssignPassageModal(passage);
      });
      head.appendChild(manageBtn);
    }

    card.appendChild(head);

    // The ordered 自然段: one row per paragraph keeps the original's own
    // boundaries and order; a paragraph inside ANY chosen range reads
    // marked so 选择第 2、3 段 visibly leaves the others in place.
    var covered = {};
    (passage.scopes || []).forEach(function (s) {
      (s.paragraphs || []).forEach(function (no) { covered[no] = true; });
      if (s.whole) {
        (passage.paragraphs || []).forEach(function (_, i) { covered[i + 1] = true; });
      }
    });
    var paragraphs = document.createElement('div');
    paragraphs.className = 'passage-paragraphs';
    (passage.paragraphs || []).forEach(function (text, i) {
      var no = i + 1;
      var line = document.createElement('p');
      line.className = 'passage-paragraph' + (covered[no] ? ' passage-paragraph-in-scope' : '');
      var noLabel = document.createElement('span');
      noLabel.className = 'passage-paragraph-no';
      noLabel.textContent = '第 ' + no + ' 段';
      var body = document.createElement('span');
      body.className = 'passage-paragraph-text';
      body.textContent = text;
      line.appendChild(noLabel);
      line.appendChild(body);
      paragraphs.appendChild(line);
    });
    card.appendChild(paragraphs);

    // The practice ranges: existing rows with their own 移除 button, and
    // the add/re-select control (整篇 checkbox + one checkbox per
    // paragraph + 添加). Re-adding the same selection is idempotent — the
    // server reports existing and never duplicates.
    var scopesBox = document.createElement('div');
    scopesBox.className = 'passage-scopes';
    var scopesLabel = document.createElement('span');
    scopesLabel.className = 'passage-scopes-label';
    scopesLabel.textContent = '练习范围';
    scopesBox.appendChild(scopesLabel);

    if (!this.addPassageScope && !this.removePassageScope) {
      var noManage = document.createElement('span');
      noManage.className = 'passage-scope-row-text';
      noManage.textContent = (passage.scopes || []).length
        ? (passage.scopes || []).map(passageScopeText).join('；')
        : '（未选择）';
      scopesBox.appendChild(noManage);
    } else {
      (passage.scopes || []).forEach(function (s) {
        var row = document.createElement('span');
        row.className = 'passage-scope-row';
        row.setAttribute('data-scope-id', String(s.id));
        var text = document.createElement('span');
        text.className = 'passage-scope-row-text';
        text.textContent = passageScopeText(s);
        row.appendChild(text);
        // Issue #14: the range's OWN practice state — mastered and the next
        // review day stay per range, never borrowed across ranges.
        var practiceLine = passageScopePracticeLine(s);
        if (practiceLine) {
          var state = document.createElement('span');
          state.className = 'passage-scope-practice' + (s.last_mastered ? ' passage-scope-practiced' : '');
          state.textContent = practiceLine;
          row.appendChild(state);
        }
        // Issue #14: the per-range 背默 door — each stored range is an
        // independent practice entry; an out-of-scope passage keeps the
        // honest disabled reason (the same gate the classroom enforces).
        var reciteBtn = document.createElement('button');
        reciteBtn.type = 'button';
        reciteBtn.className = 'row-action passage-scope-recite';
        if (passage.in_scope) {
          reciteBtn.textContent = passageScopeReciteCopy(s);
          reciteBtn.title = '打开学生页，凭记忆默写' + passageScopeText(s);
          reciteBtn.addEventListener('click', function () {
            global.open('/tutor/?passage=' + encodeURIComponent(passage.id) +
              '&scope=' + encodeURIComponent(s.id) +
              '&category=' + (passage.category === 'chinese' ? 'chinese' : 'english'), '_blank');
          });
        } else {
          reciteBtn.textContent = '暂不在学习范围';
          reciteBtn.disabled = true;
          reciteBtn.title = '课次尚未学到且未手动加入，暂不能开始背默';
        }
        row.appendChild(reciteBtn);
        if (self.removePassageScope) {
          var removeBtn = document.createElement('button');
          removeBtn.type = 'button';
          removeBtn.className = 'row-action passage-scope-remove';
          removeBtn.textContent = '移除';
          removeBtn.setAttribute('aria-label', '移除练习范围 ' + passageScopeText(s));
          removeBtn.addEventListener('click', function () {
            removeBtn.disabled = true;
            self.removePassageScope(passage.id, s.id).then(function () {
              toast('已移除练习范围「' + passageScopeText(s) + '」，课文原文和其他范围不受影响', 'success');
              self.refresh();
              self.onMutated();
            }).catch(function (err) {
              removeBtn.disabled = false;
              toast('移除失败：' + errorMessage(err) + '。请重试。', 'error');
            });
          });
          row.appendChild(removeBtn);
        }
        scopesBox.appendChild(row);
      });

      if (this.addPassageScope) {
        var addBox = document.createElement('div');
        addBox.className = 'passage-scope-add';
        var wholeHit = document.createElement('label');
        wholeHit.className = 'inline-check-hit';
        var wholeCheck = document.createElement('input');
        wholeCheck.type = 'checkbox';
        wholeCheck.setAttribute('aria-label', '添加整篇练习范围');
        var wholeText = document.createElement('span');
        wholeText.textContent = '整篇';
        wholeHit.appendChild(wholeCheck);
        wholeHit.appendChild(wholeText);
        addBox.appendChild(wholeHit);

        var paragraphChecks = [];
        (passage.paragraphs || []).forEach(function (_, i) {
          var no = i + 1;
          var hit = document.createElement('label');
          hit.className = 'inline-check-hit passage-scope-para-hit';
          var check = document.createElement('input');
          check.type = 'checkbox';
          check.setAttribute('aria-label', '添加第 ' + no + ' 段练习范围');
          check.addEventListener('change', function () {
            if (check.checked) {
              wholeCheck.checked = false;
            }
          });
          var noText = document.createElement('span');
          noText.textContent = String(no);
          hit.appendChild(check);
          hit.appendChild(noText);
          addBox.appendChild(hit);
          paragraphChecks.push({ no: no, check: check });
        });
        wholeCheck.addEventListener('change', function () {
          if (wholeCheck.checked) {
            paragraphChecks.forEach(function (p) { p.check.checked = false; });
          }
        });

        var addBtn = document.createElement('button');
        addBtn.type = 'button';
        addBtn.className = 'row-action passage-scope-add-btn';
        addBtn.textContent = '添加练习范围';
        addBtn.addEventListener('click', function () {
          var payload = null;
          if (wholeCheck.checked) {
            payload = { whole: true };
          } else {
            var chosen = paragraphChecks
              .filter(function (p) { return p.check.checked; })
              .map(function (p) { return p.no; });
            if (!chosen.length) {
              toast('请先勾选整篇或要练习的自然段', 'error');
              return;
            }
            payload = { paragraphs: chosen };
          }
          var label = passageScopePayloadText(payload);
          addBtn.disabled = true;
          self.addPassageScope(passage.id, payload).then(function (result) {
            toast((!result || result.status !== 'existing')
              ? '已添加练习范围「' + label + '」'
              : '练习范围「' + label + '」已存在，不会重复建立', 'success');
            self.refresh();
            self.onMutated();
          }).catch(function (err) {
            addBtn.disabled = false;
            toast('添加失败：' + errorMessage(err) + '。请重试。', 'error');
          });
        });
        addBox.appendChild(addBtn);
        scopesBox.appendChild(addBox);
      }
    }
    card.appendChild(scopesBox);

    // Attribution chips — the poem card's chip shape, plus a 手动 marker
    // on assignments the parent explicitly included.
    (passage.textbooks || []).forEach(function (entry) {
      var chip = document.createElement('span');
      chip.className = 'textbook-chip' + (entry.lesson ? '' : ' textbook-chip-pending');
      chip.textContent = (entry.name || '？') +
        (entry.lesson ? ('·' + entry.lesson) : '·待确认') +
        (entry.includeInScope ? '（手动加入）' : '');
      chip.title = entry.includeInScope
        ? '已手动加入学习范围：无论进度如何都在范围内'
        : (entry.lesson
          ? '教材课次归属：' + (entry.name || '') + '（随已学进度进入范围）'
          : '课次待确认：暂不进入学习范围，确认课次或手动加入后生效');
      card.appendChild(chip);
    });

    return card;
  };

  /** passageScopePayloadText — the ADD payload as parent copy. */
  function passageScopePayloadText(payload) {
    return payload && payload.whole
      ? '整篇'
      : ('第 ' + (payload.paragraphs || []).join('、') + ' 段');
  }

  /**
   * openAssignPassageModal — the 课文 page's explicit attribution
   * management, the openAssignPoemModal twin (pick the passage's OWN
   * subject's textbook, set the lesson and the explicit include together
   * — POST /passages/{id}/assign carries the whole desired state).
   */
  BrowseView.prototype.openAssignPassageModal = function (passage) {
    var self = this;

    openModal({
      title: '管理课文归属',
      build: function (body, actions, close) {
        var form = document.createElement('form');
        form.className = 'modal-form';
        form.noValidate = true;

        var passageLine = document.createElement('p');
        passageLine.className = 'modal-message';
        passageLine.textContent = '「' + (passage.title || '（无标题）') + '」' +
          (passage.author ? '（' + passage.author + '）' : '');
        form.appendChild(passageLine);

        var bookField = document.createElement('div');
        var bookLabel = document.createElement('label');
        bookLabel.className = 'form-label';
        bookLabel.textContent = '教材';
        var bookSelect = document.createElement('select');
        bookSelect.className = 'form-input';
        bookSelect.setAttribute('aria-label', '教材');
        var loadingOption = document.createElement('option');
        loadingOption.textContent = '读取教材中…';
        bookSelect.appendChild(loadingOption);
        bookField.appendChild(bookLabel);
        bookField.appendChild(bookSelect);
        form.appendChild(bookField);

        var lessonField = buildModalField('课次', 'passage-lesson-input', false);
        lessonField.input.value = (passage.textbooks && passage.textbooks.length)
          ? (passage.textbooks[0].lesson || '') : '';
        form.appendChild(lessonField.field);

        var includeField = document.createElement('div');
        includeField.className = 'form-field';
        var includeLabel = document.createElement('label');
        includeLabel.className = 'participation-toggle';
        var include = document.createElement('input');
        include.type = 'checkbox';
        include.checked = !!(passage.textbooks && passage.textbooks[0] &&
          passage.textbooks[0].includeInScope);
        var includeText = document.createElement('span');
        includeText.textContent = '手动加入学习范围（不受已学进度限制）';
        includeLabel.appendChild(include);
        includeLabel.appendChild(includeText);
        includeField.appendChild(includeLabel);
        form.appendChild(includeField);

        var error = document.createElement('p');
        error.className = 'form-error';
        error.setAttribute('aria-live', 'polite');
        form.appendChild(error);

        // The textbook list rides the passage's OWN subject; a failed load
        // keeps the modal open with its reason (never a fabricated empty
        // list).
        self.fetchTextbooks(passage.category || '').then(function (books) {
          clearContainer(bookSelect);
          (books || []).forEach(function (book) {
            var opt = document.createElement('option');
            opt.value = String(book.id);
            opt.textContent = book.name;
            bookSelect.appendChild(opt);
          });
          if (!books || !books.length) {
            var none = document.createElement('option');
            none.value = '';
            none.textContent = '（该学科还没有教材）';
            bookSelect.appendChild(none);
            bookSelect.disabled = true;
            error.textContent = '该学科还没有教材，请先在「教材管理」中建立教材。';
            return;
          }
          // Preselect the passage's current attribution when present.
          if (passage.textbooks && passage.textbooks.length) {
            bookSelect.value = String(passage.textbooks[0].id);
          }
        }).catch(function (err) {
          clearContainer(bookSelect);
          var failed = document.createElement('option');
          failed.value = '';
          failed.textContent = '（教材读取失败）';
          bookSelect.appendChild(failed);
          bookSelect.disabled = true;
          error.textContent = '教材读取失败：' + errorMessage(err);
        });

        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);

        var saveBtn = buildLoadingButton('保存归属', 'btn btn-primary');
        saveBtn.type = 'submit';

        actions.appendChild(cancelBtn);
        actions.appendChild(saveBtn);
        form.appendChild(actions);
        body.appendChild(form);

        function handleSave() {
          if (saveBtn.disabled) {
            return;
          }
          error.textContent = '';
          var bookId = parseInt(bookSelect.value, 10);
          if (!bookId) {
            error.textContent = '请选择教材（或先在「教材管理」中建立教材）';
            return;
          }
          setLoading(saveBtn, true);
          self.assignPassage(passage.id, {
            textbook_id: bookId,
            lesson: lessonField.input.value.trim(),
            includeInScope: include.checked,
          }).then(function () {
            close();
            toast('已保存课文归属', 'success');
            self.refresh();
            self.onMutated();
          }).catch(function (err) {
            setLoading(saveBtn, false);
            toast('保存失败：' + errorMessage(err) + '。内容没有保存，请检查后重试。', 'error');
          });
        }

        saveBtn.addEventListener('click', function (event) {
          event.preventDefault();
          handleSave();
        });
        form.addEventListener('submit', function (event) {
          event.preventDefault();
          handleSave();
        });
      },
    });
  };

  /**
   * Edit modal (编辑单词): 单词 required (D-08 — empty marks inline),
   * category read-only (D-07 — edit never changes category), 保存修改
   * with loading state → PUT /api/v1/words/{id} → toast + refresh.
   */
  BrowseView.prototype.openEditModal = function (word) {
    var self = this;

    // The modal's 单元 input shares the ONE #unit-options datalist;
    // scope suggestions to this word's category while it is open.
    this.onUnitSuggestions(word.category || '');

    openModal({
      title: '编辑单词',
      build: function (body, actions, close) {
        var form = document.createElement('form');
        form.className = 'modal-form';
        form.noValidate = true;

        var categoryField = document.createElement('div');
        categoryField.className = 'form-field';
        var categoryLabel = document.createElement('span');
        categoryLabel.className = 'form-label';
        categoryLabel.textContent = '分类';
        var categoryBadge = document.createElement('span');
        categoryBadge.className = 'badge badge-' +
          (word.category === 'chinese' ? 'chinese' : 'english');
        categoryBadge.setAttribute('data-coding', word.category);
        categoryBadge.textContent = word.category === 'chinese' ? '中文' : '英语';
        categoryField.appendChild(categoryLabel);
        categoryField.appendChild(categoryBadge);
        form.appendChild(categoryField);

        var wordField = buildModalField('单词', 'edit-word', false);
        wordField.input.value = word.word;
        var meaningField = buildModalField('意思', 'edit-meaning', false);
        meaningField.input.value = word.meaning || '';
        var exampleField = buildModalField('例句', 'edit-example', true);
        exampleField.input.value = word.example || '';
        form.appendChild(wordField.field);
        form.appendChild(meaningField.field);
        form.appendChild(exampleField.field);

        // Issue #89: once a word has textbook lesson ownership, the unit
        // tag is not a second lesson. Editing it here used to look like
        // 指定课次 had done nothing. The lesson is changed with 指定教材课次.
        var unitField = null;
        var hasLesson = (word.textbooks && word.textbooks.length) ||
          (word.scope_reason && word.scope_reason !== 'supplementary');
        if (hasLesson) {
          var lessonNote = document.createElement('p');
          lessonNote.className = 'form-hint';
          var names = (word.textbooks || []).map(function (entry) {
            return (entry.name || '教材') + (entry.lesson ? '·' + entry.lesson : '·待确认');
          }).join('、');
          var status = dictationStatusText(word);
          lessonNote.textContent = '课次以教材归属为准' + (names ? '（' + names + '）' : '') + '。' +
            (status ? status + '。' : '') + '修改课次请用「指定教材课次」。';
          form.appendChild(lessonNote);
        } else {
          unitField = buildModalField('单元', 'edit-unit', false);
          unitField.input.setAttribute('list', 'unit-options');
          unitField.input.value = word.unit || '';
          form.appendChild(unitField.field);
        }

        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);

        var saveBtn = buildLoadingButton('保存修改', 'btn btn-primary');
        saveBtn.type = 'submit';

        actions.appendChild(cancelBtn);
        actions.appendChild(saveBtn);
        // Move the actions row into the form so Enter submits.
        form.appendChild(actions);
        body.appendChild(form);

        function handleSave() {
          if (saveBtn.disabled) {
            return;
          }
          wordField.error.textContent = '';
          var newWord = wordField.input.value.trim();
          if (!newWord) {
            wordField.error.textContent = '请输入单词';
            return;
          }
          setLoading(saveBtn, true);
          var payload = {
            word: newWord,
            meaning: meaningField.input.value.trim(),
            example: exampleField.input.value,
          };
          if (unitField) {
            payload.unit = unitField.input.value.trim();
          }
          self.editWord(word.id, payload).then(function () {
            close();
            toast('已保存', 'success');
            self.refresh();
            self.onMutated();
          }).catch(function (err) {
            setLoading(saveBtn, false);
            toast('保存失败：' + errorMessage(err) + '。内容没有保存，请检查后重试。', 'error');
          });
        }

        saveBtn.addEventListener('click', function (event) {
          event.preventDefault();
          handleSave();
        });
        form.addEventListener('submit', function (event) {
          event.preventDefault();
          handleSave();
        });
      },
    });
  };

  /**
   * openBatchEditModal (Issue #80 AC-09) — 批量编辑: paste the corrected
   * word list (the 批量添加 line contract) over the checked rows. The
   * PREVIEW is memory-only (previewBatchEdits); 取消 drops it untouched.
   * 确认修改 rides ONE PUT /words/{id} per non-blocked edit — a word's
   * failure fails only itself and is named in the result line, never a
   * silent partial success.
   */
  BrowseView.prototype.openBatchEditModal = function () {
    var self = this;
    if (this.type !== 'words') {
      return;
    }
    var ids = this.selectedIds;
    var selected = (this.words || []).filter(function (w) {
      return ids[String(w.id)];
    });
    if (!selected.length || selected.length > 500) {
      return;
    }

    openModal({
      wide: true,
      title: '批量编辑（已选 ' + selected.length + ' 个词）',
      build: function (body, actions, close) {
        var hint = document.createElement('p');
        hint.className = 'form-hint';
        hint.textContent = '按勾选顺序每行一个词，word<TAB>释义（无 Tab 按第一个空格分）；' +
          '行数必须与已选数量一致。改词面落到别的词的面上会被阻断，不会覆盖。';
        body.appendChild(hint);

        var paste = document.createElement('textarea');
        paste.className = 'form-textarea';
        paste.id = 'batch-edit-input';
        paste.rows = Math.min(10, Math.max(4, selected.length));
        paste.setAttribute('aria-label', '批量编辑粘贴');
        var prefilled = selected.map(function (w) {
          return w.word + (w.meaning ? '\t' + w.meaning : '');
        }).join('\n');
        paste.value = prefilled;
        body.appendChild(paste);

        var previewBox = document.createElement('div');
        previewBox.className = 'batch-edit-preview';
        previewBox.id = 'batch-edit-preview';
        body.appendChild(previewBox);

        var applyBtn = buildLoadingButton('确认修改', 'btn btn-primary');
        applyBtn.type = 'button';
        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);
        actions.appendChild(cancelBtn);
        actions.appendChild(applyBtn);

        // The preview re-derives from the CURRENT text on every input —
        // pure functions only, nothing written until 确认修改.
        var render = function () {
          var parsed = global.parseBatch(paste.value);
          var preview = global.previewBatchEdits(selected, parsed.rows, self.words);
          clearContainer(previewBox);
          previewBox.dataset.ok = preview.ok ? 'true' : 'false';
          if (!preview.ok && preview.code === 'count-mismatch') {
            var mismatch = document.createElement('p');
            mismatch.className = 'batch-edit-problem';
            mismatch.textContent = '行数不一致：粘贴 ' + preview.pasted + ' 行，已选 ' +
              preview.selected + ' 个词。请补齐或删减到一致（# 开头的行和空行不计）。';
            previewBox.appendChild(mismatch);
            applyBtn.disabled = true;
            return;
          }
          var edits = preview.edits.filter(function (e) { return e.kind === 'edit'; });
          var blocked = global.blockedEdits(preview);
          (parsed.errors || []).forEach(function (err) {
            var line = document.createElement('p');
            line.className = 'batch-edit-problem';
            line.textContent = '第 ' + err.line + ' 行无法使用：' +
              batchEditErrorText(err.reason) + '。';
            previewBox.appendChild(line);
          });
          var summary = document.createElement('p');
          summary.className = 'batch-edit-summary';
          summary.textContent = edits.length + ' 处修改、' + preview.unchanged +
            ' 处不变' + (blocked.length ? '、' + blocked.length + ' 处阻断' : '') + '。';
          previewBox.appendChild(summary);
          var list = document.createElement('ul');
          list.className = 'batch-edit-list';
          edits.forEach(function (e) {
            var li = document.createElement('li');
            var face = e.wordTo === e.wordFrom ? e.wordFrom
              : (e.wordFrom + ' → ' + e.wordTo);
            li.textContent = face + '（释义：' +
              (e.meaningTo === e.meaningFrom ? '不变' : ('「' + (e.meaningFrom || '（空）') +
                '」→「' + e.meaningTo + '」')) + '）' +
              (e.blocked === 'identity-conflict'
                ? ' — 已阻断：词面与另一个词（#' + e.conflictId + '）相同'
                : (e.blocked === 'meaning-too-long' ? ' — 已阻断：释义超过 1000 字' : ''));
            if (e.blocked) {
              li.className = 'batch-edit-blocked';
            }
            list.appendChild(li);
          });
          previewBox.appendChild(list);
          applyBtn.disabled = !edits.some(function (e) { return !e.blocked; });
        };
        paste.addEventListener('input', render);
        render();

        applyBtn.addEventListener('click', function () {
          if (applyBtn.disabled) {
            return;
          }
          var parsed = global.parseBatch(paste.value);
          var preview = global.previewBatchEdits(selected, parsed.rows, self.words);
          var pending = preview.edits.filter(function (e) {
            return e.kind === 'edit' && !e.blocked;
          });
          if (!pending.length) {
            return;
          }
          setLoading(applyBtn, true);
          var failures = [];
          // ONE PUT per word, sequential: each editWord is its own call —
          // a failure fails only itself and the loop keeps going.
          pending.reduce(function (chain, e) {
            return chain.then(function () {
              // example/unit stay untouched: absent flags keep the stored
              // values server-side (cmd_edit's COALESCE contract).
              return self.editWord(e.id, { word: e.wordTo, meaning: e.meaningTo })
                .catch(function (err) {
                  failures.push({ word: e.wordFrom, error: errorMessage(err) });
                });
            });
          }, Promise.resolve()).then(function () {
            close();
            self.selectedIds = {};
            if (failures.length) {
              toast('已保存 ' + (pending.length - failures.length) + ' 个，失败 ' +
                failures.length + ' 个：' + failures.map(function (f) {
                  return f.word + '（' + f.error + '）';
                }).join('、') + '。请重试失败的部分。', 'error');
            } else {
              toast('已保存 ' + pending.length + ' 处修改', 'success');
            }
            self.refresh();
            self.onMutated();
          });
        });
      },
    });
  };

  /** One parse-error reason's human line (the batch parser's codes). */
  function batchEditErrorText(reason) {
    var code = String(reason || '').split(':')[0];
    if (code === 'empty-word') {
      return '词面为空';
    }
    if (code === 'word-comma') {
      return '词面里不能有英文逗号';
    }
    if (code === 'word-too-long') {
      return '词面超过 200 字';
    }
    if (code === 'duplicate') {
      return '与第 ' + String(reason).split(':')[1] + ' 行重复';
    }
    return '无法解析';
  }

  // ------------------------------------------------------------------
  // Rename + destructive-delete flows (Plan 09-07 Task 2). Every
  // mutation is gated by its modal — the dialog is the ONLY confirmation
  // layer and the API call issued is always the confirmed one (prohibition
  // pairing with Plan 09-05: no delete request exists outside this path).
  // ------------------------------------------------------------------

  /** Run one mutation per category, sequentially, failing fast. */
  BrowseView.prototype.runPerCategory = function (categories, perform) {
    return categories.reduce(function (chain, category) {
      return chain.then(function () {
        return perform(category);
      });
    }, Promise.resolve());
  };

  /**
   * Rename modal (重命名单元): 将「{old}」重命名为 + input + 确认重命名
   * / 取消 (loading state on confirm) → POST /api/v1/units/rename with
   * the group's category scope, or per-row edits when the unit string is
   * also worn by lesson-owned cards. A not_found response surfaces the
   * backend message in an Error toast (Pitfall 2); generic failures show
   * 保存失败：{message}。
   */
  BrowseView.prototype.openRenameModal = function (group) {
    var self = this;
    var unit = group.unit;
    var rows = group.rows || [];
    var touchesLesson = unitRenameTouchesLesson(this.words, unit);
    var categories = [];
    rows.forEach(function (word) {
      if (word && word.category && categories.indexOf(word.category) === -1) {
        categories.push(word.category);
      }
    });

    openModal({
      title: '重命名单元',
      build: function (body, actions, close) {
        var form = document.createElement('form');
        form.className = 'modal-form';
        form.noValidate = true;

        var prompt = document.createElement('p');
        prompt.className = 'modal-message';
        prompt.textContent = '将「' + unit + '」重命名为';
        form.appendChild(prompt);

        var field = buildModalField('新单元名', 'rename-unit-input', false);
        field.input.value = unit;
        form.appendChild(field.field);

        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);

        var confirmBtn = buildLoadingButton('确认重命名', 'btn btn-primary');
        confirmBtn.type = 'submit';

        actions.appendChild(cancelBtn);
        actions.appendChild(confirmBtn);
        form.appendChild(actions);
        body.appendChild(form);

        function handleConfirm() {
          if (confirmBtn.disabled) {
            return;
          }
          field.error.textContent = '';
          var newName = field.input.value.trim();
          if (!newName) {
            field.error.textContent = '请输入新的单元名';
            return;
          }
          if (newName === unit) {
            close();
            return;
          }
          setLoading(confirmBtn, true);
          var rename;
          if (touchesLesson) {
            // The unit string is also worn by lesson-owned cards. Rewrite
            // only this group's rows so those cards keep the tag the page
            // no longer groups by.
            rename = rows.reduce(function (chain, word) {
              return chain.then(function () {
                return self.editWord(word.id, { word: word.word, unit: newName });
              });
            }, Promise.resolve());
          } else {
            rename = self.runPerCategory(categories, function (category) {
              return self.renameUnit(unit, newName, category);
            });
          }
          rename.then(function () {
            close();
            toast('已重命名', 'success');
            self.refresh();
            self.onMutated();
          }).catch(function (err) {
            setLoading(confirmBtn, false);
            if (err && err.code === 'not_found') {
              // Pitfall 2 surfaced: the backend's stdout diagnostic
              // rides as the message — show it verbatim.
              toast(errorMessage(err), 'error');
            } else {
              toast('保存失败：' + errorMessage(err) + '。', 'error');
            }
            // One category may have renamed before a later one failed —
            // re-render the true state either way.
            self.refresh();
          });
        }

        confirmBtn.addEventListener('click', function (event) {
          event.preventDefault();
          handleConfirm();
        });
        form.addEventListener('submit', function (event) {
          event.preventDefault();
          handleConfirm();
        });
      },
    });
  };

  /**
   * Destructive delete-word confirm (删除单词): exact-word copy +
   * 确认删除 / 取消 → DELETE /api/v1/words with {category, words:[word]}.
   */
  BrowseView.prototype.openDeleteWordModal = function (word) {
    var self = this;
    openModal({
      title: '删除单词',
      danger: true,
      build: function (body, actions, close) {
        var message = document.createElement('p');
        message.className = 'modal-message';
        message.textContent = '确认删除「' + word.word + '」？它的学习记录会一起删除，无法恢复。';
        body.appendChild(message);

        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);

        var confirmBtn = buildLoadingButton('确认删除', 'btn btn-destructive');
        confirmBtn.addEventListener('click', function () {
          if (confirmBtn.disabled) {
            return;
          }
          setLoading(confirmBtn, true);
          self.deleteWords({ category: word.category, words: [word.word] })
            .then(function () {
              close();
              toast('已删除', 'success');
              self.refresh();
              self.onMutated();
            })
            .catch(function (err) {
              setLoading(confirmBtn, false);
              toast('删除失败：' + errorMessage(err) + '。', 'error');
            });
        });

        actions.appendChild(cancelBtn);
        actions.appendChild(confirmBtn);
      },
    });
  };

  /**
   * Destructive delete-unit confirm (删除单元): exact-count copy (n from
   * the badge) → DELETE /api/v1/words with the group's own word list.
   * {category, unit} would also delete lesson-owned cards that still
   * wear the same unit string (Issue #89). Failure keeps the dialog
   * open with the backend message and re-renders the true state.
   */
  BrowseView.prototype.openDeleteUnitModal = function (group) {
    var self = this;
    var unit = group.unit;
    var plan = supplementaryDeletePlan(group);
    var count = plan.count;

    openModal({
      title: '删除单元',
      danger: true,
      build: function (body, actions, close) {
        var message = document.createElement('p');
        message.className = 'modal-message';
        message.textContent = '确认删除「' + unit + '」的全部 ' + count +
          ' 个单词？学习记录会一起删除，无法恢复。';
        body.appendChild(message);

        var cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-secondary';
        cancelBtn.textContent = '取消';
        cancelBtn.addEventListener('click', close);

        var confirmBtn = buildLoadingButton('确认删除', 'btn btn-destructive');
        confirmBtn.addEventListener('click', function () {
          if (confirmBtn.disabled) {
            return;
          }
          setLoading(confirmBtn, true);
          var deleted = 0;
          plan.requests.reduce(function (chain, req) {
            return chain.then(function () {
              return self.deleteWords({ category: req.category, words: req.words })
                .then(function (result) {
                  deleted += (result && result.deleted) || 0;
                });
            });
          }, Promise.resolve()).then(function () {
            close();
            toast('已删除 ' + deleted + ' 个单词', 'success');
            self.refresh();
            self.onMutated();
          }).catch(function (err) {
            setLoading(confirmBtn, false);
            toast('删除失败：' + errorMessage(err) + '。', 'error');
            self.refresh();
          });
        });

        actions.appendChild(cancelBtn);
        actions.appendChild(confirmBtn);
      },
    });
  };

  // Window-or-module-global tail (state-manager.js convention) so Node
  // tests can require this file later.
  global.BrowseView = BrowseView;
  global.groupBrowseWords = groupBrowseWords;
  global.orderBrowseGroups = orderBrowseGroups;
  global.dictationStatusText = dictationStatusText;
  global.wordNeedsScopeRecover = wordNeedsScopeRecover;
  global.lessonOwnership = lessonOwnership;
  global.supplementaryDeletePlan = supplementaryDeletePlan;
  global.unitRenameTouchesLesson = unitRenameTouchesLesson;
  // Shared modal builders for sibling components (parent-textbook.js
  // mounts its modals through the same focus-trapped host — the
  // __parentFormKit precedent).
  global.__parentModalKit = {
    openModal: openModal,
    buildModalField: buildModalField,
  };
})(typeof window !== 'undefined' ? window : this);
