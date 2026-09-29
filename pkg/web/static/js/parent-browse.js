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
 *     count badge {n} 个单词 from GET /api/v1/units); collapse state
 *     persists in localStorage under parent.units.collapsed (JSON array
 *     of unit names, re-applied on every render).
 *   - Groups are built client-side from the word list; the grouping key
 *     is the whole unit string (flat strings, prefix-agnostic per D-09).
 *     Groups left with zero rows by the search are hidden, not rendered
 *     empty (zero-one-many row).
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

  /** Header label for rows whose unit is the empty string. */
  var UNGROUPED_LABEL = '未分组';

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
    dialog.className = 'modal' + (spec.danger ? ' modal-danger' : '');
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
    this.renameUnit = options.renameUnit || function () {
      return Promise.reject(new Error('BrowseView: missing renameUnit handler'));
    };
    this.deleteWords = options.deleteWords || function () {
      return Promise.reject(new Error('BrowseView: missing deleteWords handler'));
    };
    this.onMutated = options.onMutated || function () {};
    this.onUnitSuggestions = options.onUnitSuggestions || function () {};

    this.container = container;
    this.category = '';   // '' = 全部 | 'english' | 'chinese'
    this.query = '';
    this.words = [];
    this.units = [];
    this.fetchSeq = 0;    // stale-response guard for debounced refetches
    this.debounceTimer = null;

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

    // The browse list region announces polite updates (UI-SPEC
    // Accessibility Contract).
    this.listHost = document.createElement('div');
    this.listHost.className = 'browse-list';
    this.listHost.setAttribute('aria-live', 'polite');

    this.container.appendChild(toolbar);
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
   * Re-fetch words + unit counts and re-render. The sequence token
   * drops stale responses when a debounced search races a pill switch.
   */
  BrowseView.prototype.refresh = function () {
    var seq = ++this.fetchSeq;
    this.renderSkeleton();
    var self = this;
    return Promise.all([
      this.fetchWords(this.category, this.query),
      this.fetchUnits(this.category),
    ]).then(function (results) {
      if (seq !== self.fetchSeq) {
        return;
      }
      self.words = Array.isArray(results[0]) ? results[0] : [];
      self.units = Array.isArray(results[1]) ? results[1] : [];
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

    if (!this.words.length) {
      this.renderEmptyState();
      return;
    }

    var groups = this.groupWords();
    var self = this;
    groups.forEach(function (group) {
      self.listHost.appendChild(self.buildGroup(group));
    });
  };

  /**
   * Empty states (UI Considerations empty/library + empty/search):
   * zero words → 词库还是空的 + guidance copy; no matches → 没有找到
   * 「{q}」相关的单词 + 换个关键词试试.
   */
  BrowseView.prototype.renderEmptyState = function () {
    var title = document.createElement('p');
    title.className = 'list-empty list-empty-title';
    var hint = document.createElement('p');
    hint.className = 'list-empty list-empty-hint';

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
   * Group the word list client-side. The grouping key is the whole unit
   * string (D-09 flat strings, prefix-agnostic); rows whose unit is the
   * empty string land in one 未分组 group. Order follows the server's.
   */
  BrowseView.prototype.groupWords = function () {
    var groups = [];
    var index = {};
    this.words.forEach(function (word) {
      var key = word.unit || '';
      if (!Object.prototype.hasOwnProperty.call(index, key)) {
        index[key] = { unit: key, rows: [] };
        groups.push(index[key]);
      }
      index[key].rows.push(word);
    });
    return groups;
  };

  /** Count-badge value for a unit from the GET /units response, or null. */
  BrowseView.prototype.countForUnit = function (unit) {
    for (var i = 0; i < this.units.length; i++) {
      var entry = this.units[i];
      if (entry && entry.unit === unit && typeof entry.count === 'number') {
        return entry.count;
      }
    }
    return null;
  };

  /**
   * Unit ops act on {category, unit} server-side. In the 全部 view with
   * an active search, the group's visible rows may only cover SOME of
   * the categories holding that unit — renaming/deleting from the badge
   * count then would not match the dialog's exact-scope copy (T-09-05),
   * so the header actions are withheld in that one combination. Word
   * rows keep their per-word 编辑/删除 everywhere.
   */
  BrowseView.prototype.unitOpsAllowed = function () {
    return this.query === '' || this.category !== '';
  };

  BrowseView.prototype.buildGroup = function (group) {
    var self = this;
    var unit = group.unit;

    var wrap = document.createElement('div');
    wrap.className = 'unit-group';
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
    name.textContent = unit || UNGROUPED_LABEL;

    // Count badge: {n} 个单词 from GET /api/v1/units (category-scoped;
    // the all-categories view merges by name). The 未分组 group has no
    // units entry — its row count is the honest count there.
    var badge = document.createElement('span');
    badge.className = 'unit-count';
    var count = unit !== '' ? this.countForUnit(unit) : group.rows.length;
    if (count === null) {
      count = group.rows.length;
    }
    badge.textContent = count + ' 个单词';

    toggle.appendChild(chevron);
    toggle.appendChild(name);
    toggle.appendChild(badge);
    header.appendChild(toggle);

    var body = document.createElement('div');
    body.className = 'unit-body';
    group.rows.forEach(function (word) {
      body.appendChild(self.buildRow(word));
    });

    function applyCollapse(names) {
      var isCollapsed = names.indexOf(unit) !== -1;
      body.hidden = isCollapsed;
      chevron.textContent = isCollapsed ? '▸' : '▾';
      toggle.setAttribute('aria-expanded', isCollapsed ? 'false' : 'true');
    }
    applyCollapse(readCollapsedNames());

    toggle.addEventListener('click', function () {
      var names = readCollapsedNames();
      var idx = names.indexOf(unit);
      if (idx === -1) {
        names.push(unit);
      } else {
        names.splice(idx, 1);
      }
      writeCollapsedNames(names);
      applyCollapse(names);
    });

    wrap.appendChild(header);
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

    var text = document.createElement('span');
    text.className = 'word-text';
    text.textContent = word.word;

    var meaning = document.createElement('span');
    meaning.className = 'word-meaning';
    meaning.textContent = word.meaning || '';

    var badge = document.createElement('span');
    badge.className = 'badge badge-' + (word.category === 'chinese' ? 'chinese' : 'english');
    badge.setAttribute('data-coding', word.category);
    badge.textContent = word.category === 'chinese' ? '中文' : '英语';

    var actions = document.createElement('span');
    actions.className = 'row-actions';

    var editBtn = document.createElement('button');
    editBtn.type = 'button';
    editBtn.className = 'row-action';
    editBtn.textContent = '编辑';
    editBtn.addEventListener('click', function () {
      self.openEditModal(word);
    });
    actions.appendChild(editBtn);

    row.appendChild(text);
    if (word.meaning) {
      row.appendChild(meaning);
    }
    row.appendChild(badge);
    row.appendChild(actions);
    return row;
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
        var unitField = buildModalField('单元', 'edit-unit', false);
        unitField.input.setAttribute('list', 'unit-options');
        unitField.input.value = word.unit || '';

        form.appendChild(wordField.field);
        form.appendChild(meaningField.field);
        form.appendChild(exampleField.field);
        form.appendChild(unitField.field);

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
          self.editWord(word.id, {
            word: newWord,
            meaning: meaningField.input.value.trim(),
            example: exampleField.input.value,
            unit: unitField.input.value.trim(),
          }).then(function () {
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

  // Window-or-module-global tail (state-manager.js convention) so Node
  // tests can require this file later.
  global.BrowseView = BrowseView;
})(typeof window !== 'undefined' ? window : this);
