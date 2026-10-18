/**
 * @file Parent Vocabulary Management — application entry point
 *       (Phase 9, Plans 01 + 06 + 07).
 *
 * Loaded with `defer` from parent.html after parent-batch-parser.js,
 * parent-modes.js and parent-browse.js so every constructor is available
 * (mirrors app.js IIFE + window.X availability-check pattern).
 *
 * Responsibilities:
 *   0. Wire the page-level tabs (13.3-03: 学习记录 | 词库 | 设置) —
 *      aria-selected + hidden-attribute panel switching, 学习记录 default
 *      on every load (state not persisted). Runs before the component
 *      checks so tabs survive a degraded page.
 *   1. Provide the fetchJSON wrapper: AbortController timeout +
 *      normalization of the {"error":{code,message}} envelope into a
 *      thrown Error carrying the message (error.code rides along so the
 *      browse modals can special-case not_found — Pitfall 2).
 *   2. Mount AddCard into #add-form-container (添加单词 card: 单个添加 |
 *      批量添加 tabs, Plan 09-06 D-10).
 *   3. Mount BrowseView into #word-list-container (浏览词库: toolbar +
 *      unit groups + edit/rename/delete modals, Plan 09-07).
 *   3b. Mount SettingsCard into #settings-container (学习设置 card,
 *      D-15) — mounting unhides the section.
 *   3c. Mount LessonsView into #lessons-container (学习记录 tab, 13.3-03)
 *      — GET /api/v1/lessons list + per-lesson detail; a missing
 *      LessonsView degrades WITHOUT blocking the other components.
 *   4. Populate the SHARED 单元 datalist (#unit-options) from
 *      GET /api/v1/units — on init (all categories), whenever an
 *      add-form category radio changes, and for the edit modal's
 *      单元 suggestions scoped to the edited word's category.
 *   5. After a successful add OR mutation, refresh the browse view and
 *      the datalist so changes appear without a page reload.
 */

(function parentAppEntry() {
  'use strict';

  // ------------------------------------------------------------------
  // Page-level tabs (13.3-03 Task 1): 学习记录 | 词库 | 设置. Switching
  // toggles each tab's aria-selected plus the target panel's hidden
  // attribute — the settingsSection hidden-attribute precedent, never
  // display:none inline styles. The default tab is 学习记录 on EVERY
  // load (explicit re-activation below); tab state is deliberately NOT
  // persisted across reloads. This wiring runs BEFORE the component
  // dependency checks so the tabs keep working even in a degraded page.
  // ------------------------------------------------------------------
  var pageTabs = Array.prototype.slice.call(
    document.querySelectorAll('.parent-tabs [role="tab"]'));
  if (!pageTabs.length) {
    console.error('[parent-app] page tab bar missing from parent.html.');
    return;
  }

  function activatePageTab(activeTab) {
    pageTabs.forEach(function (tab) {
      var active = tab === activeTab;
      tab.setAttribute('aria-selected', active ? 'true' : 'false');
      tab.classList.toggle('active', active);
      var panel = document.getElementById(tab.getAttribute('aria-controls'));
      if (panel) {
        panel.hidden = !active;
      }
    });
  }

  pageTabs.forEach(function (tab) {
    tab.addEventListener('click', function () {
      activatePageTab(tab);
    });
  });
  // Default tab on every load: the first one (学习记录) — explicit so the
  // HTML defaults and the JS state can never disagree.
  activatePageTab(pageTabs[0]);

  // ------------------------------------------------------------------
  // Dependency availability checks (graceful degradation, app.js pattern)
  // ------------------------------------------------------------------
  if (!window.AddCard || !window.BrowseView || !window.SettingsCard) {
    console.error('[parent-app] AddCard/BrowseView/SettingsCard unavailable — component scripts did not load.');
    return;
  }

  var addContainer = document.getElementById('add-form-container');
  var browseContainer = document.getElementById('word-list-container');
  var settingsContainer = document.getElementById('settings-container');
  var lessonsContainer = document.getElementById('lessons-container');
  var unitDatalist = document.getElementById('unit-options');
  if (!addContainer || !browseContainer || !settingsContainer || !lessonsContainer || !unitDatalist) {
    console.error('[parent-app] mount containers / shared datalist missing from parent.html.');
    return;
  }

  var REQUEST_TIMEOUT_MS = 15000;

  /**
   * parseEnvelope — shared response normalization for the REST wrappers:
   * reads the body text, parses JSON when possible, and on a non-2xx turns
   * the {"error":{code,message}} envelope into a thrown Error carrying
   * error.message (toast copy surfaces the backend message directly,
   * Pitfall 2) and error.code (not_found handling in the browse modals).
   * Resolves with the parsed body on 2xx (null for an empty body).
   */
  function parseEnvelope(response) {
    return response.text().then(function (text) {
      var data = null;
      if (text) {
        try {
          data = JSON.parse(text);
        } catch (e) {
          data = null;
        }
      }
      if (!response.ok) {
        var message = (data && data.error && data.error.message)
          ? data.error.message
          : ('HTTP ' + response.status);
        var error = new Error(message);
        error.status = response.status;
        error.code = (data && data.error) ? data.error.code : undefined;
        throw error;
      }
      return data;
    });
  }

  /**
   * fetchJSON — small REST wrapper for the parent page.
   *   - Aborts via AbortController after REQUEST_TIMEOUT_MS.
   *   - Response handling via parseEnvelope (see there).
   */
  function fetchJSON(method, url, body) {
    var controller = new AbortController();
    var timer = setTimeout(function () {
      controller.abort();
    }, REQUEST_TIMEOUT_MS);

    var init = { method: method, signal: controller.signal };
    if (body !== undefined) {
      init.headers = { 'Content-Type': 'application/json' };
      init.body = JSON.stringify(body);
    }

    return fetch(url, init)
      .then(parseEnvelope)
      .finally(function () {
        clearTimeout(timer);
      });
  }

  /** Clear a container via the firstChild-removeChild loop (Pattern 4). */
  function clearContainer(container) {
    while (container.firstChild) {
      container.removeChild(container.firstChild);
    }
  }

  /**
   * postImage — binary-body variant of fetchJSON for the word-list photo
   * recognize call (Issue #3). The image bytes go as the raw request body
   * with their own Content-Type; response handling reuses parseEnvelope.
   * The timeout is its own (60s): a vision model thinking over an image
   * legitimately outruns fetchJSON's 15s JSON budget (the server side
   * allows 20s).
   */
  function postImage(url, blob, timeoutMs) {
    var controller = new AbortController();
    var timer = setTimeout(function () {
      controller.abort();
    }, timeoutMs || 60000);

    return fetch(url, {
      method: 'POST',
      signal: controller.signal,
      headers: { 'Content-Type': blob.type || 'application/octet-stream' },
      body: blob,
    })
      .then(parseEnvelope)
      .finally(function () {
        clearTimeout(timer);
      });
  }

  // ------------------------------------------------------------------
  // Shared 单元 datalist: GET /api/v1/units?category=<selected> on init
  // (all categories — both forms start with no selection, D-06), on
  // every add-form category radio change, and for the edit modal's
  // suggestions. Both add forms AND the edit modal share the ONE
  // datalist element; option values are plain unit names (textContent
  // only, Pitfall 10). The empty-string unit row (words with no unit)
  // carries nothing to suggest and is skipped.
  // ------------------------------------------------------------------
  var selectedCategory = '';

  function renderUnitOptions(units) {
    clearContainer(unitDatalist);
    (units || []).forEach(function (u) {
      if (!u || !u.unit) {
        return;
      }
      var option = document.createElement('option');
      option.textContent = u.unit;
      unitDatalist.appendChild(option);
    });
  }

  function refreshUnitOptions(category) {
    selectedCategory = category || '';
    var url = '/api/v1/units' +
      (selectedCategory ? ('?category=' + encodeURIComponent(selectedCategory)) : '');
    return fetchJSON('GET', url)
      .then(function (data) {
        renderUnitOptions(data && data.units);
      })
      .catch(function () {
        // Datalist suggestions are a convenience — a failed refresh
        // degrades to the previous (or empty) list; the free-text input
        // keeps working. No toast: nothing the parent must act on.
      });
  }

  // ------------------------------------------------------------------
  // Component wiring: browse view + tabbed add card, connected by the
  // refresh hooks so a successful add or mutation updates the page
  // without a reload.
  // ------------------------------------------------------------------
  // Shared word-edit call (PUT /words/{id}): the browse list's edit modal
  // and the photo form's 采用照片释义 resolution (Issue #6) are one
  // surface — text edits never touch FSRS state server-side.
  var editWord = function (id, payload) {
    return fetchJSON('PUT', '/api/v1/words/' + encodeURIComponent(id), payload);
  };

  // Issue #4/#5 教材 API wiring — ONE implementation feeds both consumers
  // (the AddCard options below and TextbookUI.init further down); only the
  // post-mutation refresh differs per consumer.
  var textbookDeps = {
    fetchTextbooks: function (category) {
      var url = '/api/v1/textbooks' +
        (category ? ('?category=' + encodeURIComponent(category)) : '');
      return fetchJSON('GET', url).then(function (data) {
        return (data && Array.isArray(data.textbooks)) ? data.textbooks : [];
      });
    },
    createTextbook: function (payload) {
      return fetchJSON('POST', '/api/v1/textbooks', payload);
    },
    updateTextbook: function (id, payload) {
      return fetchJSON('PUT', '/api/v1/textbooks/' + encodeURIComponent(id), payload);
    },
    assignWords: function (textbookId, payload) {
      return fetchJSON('POST',
        '/api/v1/textbooks/' + encodeURIComponent(textbookId) + '/assign', payload);
    },
  };

  // Issue #11 整首古诗 API wiring — ONE implementation feeds both
  // consumers (the browse view's 古诗 page and the photo form's snapshot
  // via AddCard below): the unified content list (subject + search ride
  // the query string) and the content view's explicit attribution write.
  var fetchPoems = function (category, q) {
    var params = [];
    if (category) {
      params.push('category=' + encodeURIComponent(category));
    }
    if (q) {
      params.push('q=' + encodeURIComponent(q));
    }
    var url = '/api/v1/poems' + (params.length ? '?' + params.join('&') : '');
    return fetchJSON('GET', url).then(function (data) {
      return (data && Array.isArray(data.poems)) ? data.poems : [];
    });
  };
  var assignPoem = function (id, payload) {
    return fetchJSON('POST', '/api/v1/poems/' + encodeURIComponent(id) + '/assign', payload);
  };

  // Issue #13 课文 API wiring — the passage twin of the poem block above:
  // ONE implementation feeds the photo form (commit + snapshot through
  // AddCard below); the range-management and browse-side surfaces join
  // with the content-view task below.
  var fetchPassages = function (category, q) {
    var params = [];
    if (category) {
      params.push('category=' + encodeURIComponent(category));
    }
    if (q) {
      params.push('q=' + encodeURIComponent(q));
    }
    var url = '/api/v1/passages' + (params.length ? '?' + params.join('&') : '');
    return fetchJSON('GET', url).then(function (data) {
      return (data && Array.isArray(data.passages)) ? data.passages : [];
    });
  };

  var browse = new window.BrowseView(browseContainer, {
    fetchWords: function (category, q) {
      var params = [];
      if (category) {
        params.push('category=' + encodeURIComponent(category));
      }
      if (q) {
        params.push('q=' + encodeURIComponent(q));
      }
      var url = '/api/v1/words' + (params.length ? '?' + params.join('&') : '');
      return fetchJSON('GET', url).then(function (data) {
        return (data && Array.isArray(data.words)) ? data.words : [];
      });
    },
    fetchUnits: function (category) {
      var url = '/api/v1/units' +
        (category ? ('?category=' + encodeURIComponent(category)) : '');
      return fetchJSON('GET', url).then(function (data) {
        return (data && Array.isArray(data.units)) ? data.units : [];
      });
    },
    editWord: editWord,
    setParticipation: function (ids, enabled) {
      return fetchJSON('PUT', '/api/v1/words/participation', {
        ids: ids,
        participates: enabled,
      });
    },
    renameUnit: function (oldUnit, newUnit, category) {
      return fetchJSON('POST', '/api/v1/units/rename', {
        old_unit: oldUnit,
        new_unit: newUnit,
        category: category,
      });
    },
    deleteWords: function (payload) {
      return fetchJSON('DELETE', '/api/v1/words', payload);
    },
    onMutated: function () {
      // Edit/rename/delete can change which units exist — keep the add
      // forms' unit suggestions in sync.
      refreshUnitOptions(selectedCategory);
    },
    onUnitSuggestions: function (category) {
      refreshUnitOptions(category);
    },
    // Issue #4: the textbook surfaces (parent-textbook.js modals). The
    // browse list refreshes after every textbook mutation so the
    // attribution chips and progress effects are always current.
    onManageTextbooks: function (category) {
      if (window.TextbookUI) {
        window.TextbookUI.openManager(category);
      }
    },
    onAssignTextbook: function (ids, words) {
      if (window.TextbookUI) {
        window.TextbookUI.openAssign(ids, words);
      }
    },
    // Issue #11: the unified content view's 古诗 page — the poem list
    // (subject/search scoped) and its explicit attribution write. Absent
    // ⇒ the browse view's 类型 filter degrades to the 词语 page only.
    fetchPoems: fetchPoems,
    assignPoem: assignPoem,
    fetchTextbooks: textbookDeps.fetchTextbooks,
  });

  var addCard = new window.AddCard(addContainer, {
    submitWords: function (payload) {
      return fetchJSON('POST', '/api/v1/words', payload);
    },
    fetchWords: function (category) {
      return fetchJSON('GET', '/api/v1/words?category=' + encodeURIComponent(category))
        .then(function (data) {
          return (data && Array.isArray(data.words)) ? data.words : [];
        });
    },
    // Issue #3 照片导入: the recognize call rides postImage (60s budget —
    // a vision model over an image outruns the 15s JSON timeout). Issue
    // #10: the subject rides the query string — the same endpoint reads
    // either subject's list; category '' keeps the English default.
    recognizePhoto: function (blob, category) {
      var url = '/api/v1/words/photo/recognize';
      if (category) {
        url += '?category=' + encodeURIComponent(category);
      }
      return postImage(url, blob);
    },
    // Issue #8 缺失释义补充: one batched text-LLM call per click; the 15s
    // JSON timeout covers it (a text completion is not a vision round
    // trip; a slow service surfaces as an honest generation_failed).
    generateMeanings: function (payload) {
      return fetchJSON('POST', '/api/v1/words/meaning/generate', payload);
    },
    // Issue #11 整首古诗: the mixed 语文 page's whole-poem commit and the
    // poem-library snapshot the preview classification reads (both plain
    // JSON calls under the 15s timeout).
    submitPoems: function (payload) {
      return fetchJSON('POST', '/api/v1/poems', payload);
    },
    fetchPoems: fetchPoems,
    // Issue #13 课文: the mixed 语文 page's whole-passage commit (with
    // the chosen practice range riding each draft) and the
    // passage-library snapshot the preview classification reads (both
    // plain JSON calls under the 15s timeout).
    submitPassages: function (payload) {
      return fetchJSON('POST', '/api/v1/passages', payload);
    },
    fetchPassages: fetchPassages,
    // Issue #5 教材归属: the same textbook surfaces TextbookUI uses,
    // passed through AddCard into PhotoAddForm's attribution panel.
    // Issue #6 adds editWord — the 采用照片释义 conflict resolution rides
    // the same word-edit surface the browse list already uses.
    fetchTextbooks: textbookDeps.fetchTextbooks,
    createTextbook: textbookDeps.createTextbook,
    updateTextbook: textbookDeps.updateTextbook,
    assignWords: textbookDeps.assignWords,
    editWord: editWord,
    onCommitted: function () {
      browse.refresh();
      refreshUnitOptions(selectedCategory);
    },
    onCategoryChange: function (category) {
      refreshUnitOptions(category);
    },
  });

  // 学习设置 card (D-15): GET on init, 保存设置 → PUT (Plan 09-05 API).
  var settingsCard = new window.SettingsCard(settingsContainer, {
    fetchSettings: function () {
      return fetchJSON('GET', '/api/v1/settings/fsrs');
    },
    saveSettings: function (settings) {
      return fetchJSON('PUT', '/api/v1/settings/fsrs', settings);
    },
  });

  // ------------------------------------------------------------------
  // 教材管理 (Issue #4): the textbook modals' API wiring. Reads are plain
  // GETs; writes go POST/PUT through the shared envelope. After every
  // textbook mutation the browse list refreshes — attribution chips and
  // progress effects must never go stale on screen.
  // ------------------------------------------------------------------
  if (window.TextbookUI) {
    window.TextbookUI.init({
      fetchTextbooks: textbookDeps.fetchTextbooks,
      createTextbook: textbookDeps.createTextbook,
      updateTextbook: textbookDeps.updateTextbook,
      assignWords: textbookDeps.assignWords,
      onMutated: function () {
        browse.refresh();
      },
    });
  }

  // ------------------------------------------------------------------
  // 学习记录 view (13.3-03): GET /api/v1/lessons renders the list,
  // GET /api/v1/lessons/{id} renders the detail. LessonsView missing is
  // a NON-blocking degrade: the word library and settings tabs keep
  // working, the 学习记录 tab just stays empty with a console error.
  // ------------------------------------------------------------------
  var lessons = null;
  if (!window.LessonsView) {
    console.error('[parent-app] LessonsView unavailable — 学习记录 tab degraded (词库/设置 still work).');
  } else {
    lessons = new window.LessonsView(lessonsContainer, {
      fetchLessons: function () {
        return fetchJSON('GET', '/api/v1/lessons?limit=20').then(function (data) {
          return (data && Array.isArray(data.lessons)) ? data.lessons : [];
        });
      },
      fetchLesson: function (id) {
        return fetchJSON('GET', '/api/v1/lessons/' + encodeURIComponent(id));
      },
    });
    lessons.refresh();
  }

  // Initial render: browse view + all-category unit suggestions.
  browse.refresh();
  refreshUnitOptions('');

  // Debug exposure (app.js window.__app convention).
  window.__parentApp = {
    addCard: addCard,
    browse: browse,
    settingsCard: settingsCard,
    lessons: lessons,
    fetchJSON: fetchJSON,
    refreshUnitOptions: refreshUnitOptions,
  };
})();
