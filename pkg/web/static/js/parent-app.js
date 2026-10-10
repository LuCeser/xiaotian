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
  var unitDatalist = document.getElementById('unit-options');
  if (!addContainer || !browseContainer || !settingsContainer || !unitDatalist) {
    console.error('[parent-app] mount containers / shared datalist missing from parent.html.');
    return;
  }

  var REQUEST_TIMEOUT_MS = 15000;

  /**
   * fetchJSON — small REST wrapper for the parent page.
   *   - Aborts via AbortController after REQUEST_TIMEOUT_MS.
   *   - Parses the JSON body; normalizes the {"error":{code,message}}
   *     envelope into a thrown Error carrying error.message (so toast copy
   *     can surface the backend message directly, Pitfall 2) and
   *     error.code (not_found handling in the browse modals).
   *   - Resolves with the parsed body on 2xx (null for an empty body).
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
      .then(function (response) {
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
      })
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
    editWord: function (id, payload) {
      return fetchJSON('PUT', '/api/v1/words/' + encodeURIComponent(id), payload);
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

  // Initial render: browse view + all-category unit suggestions.
  browse.refresh();
  refreshUnitOptions('');

  // Debug exposure (app.js window.__app convention).
  window.__parentApp = {
    addCard: addCard,
    browse: browse,
    settingsCard: settingsCard,
    fetchJSON: fetchJSON,
    refreshUnitOptions: refreshUnitOptions,
  };
})();
