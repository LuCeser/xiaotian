/**
 * @file Parent Vocabulary Management — application entry point
 *       (Phase 9, Plans 01 + 06).
 *
 * Loaded with `defer` from parent.html after parent-batch-parser.js,
 * parent-modes.js and parent-browse.js so every constructor is available
 * (mirrors app.js IIFE + window.X availability-check pattern).
 *
 * Responsibilities:
 *   1. Provide the fetchJSON wrapper: AbortController timeout +
 *      normalization of the {"error":{code,message}} envelope into a
 *      thrown Error carrying the message.
 *   2. Mount AddCard into #add-form-container (添加单词 card: 单个添加 |
 *      批量添加 tabs, Plan 09-06 D-10).
 *   3. Mount WordList into #word-list-container (浏览词库).
 *   4. Populate the SHARED 单元 datalist (#unit-options) from
 *      GET /api/v1/units — on init (all categories) and whenever an
 *      add-form category radio changes (scoped to that category).
 *   5. After a successful add (single OR batch), refresh the browse list
 *      and the datalist so new words/units appear without a page reload.
 */

(function parentAppEntry() {
  'use strict';

  // ------------------------------------------------------------------
  // Dependency availability checks (graceful degradation, app.js pattern)
  // ------------------------------------------------------------------
  if (!window.AddCard || !window.WordList) {
    console.error('[parent-app] AddCard/WordList unavailable — component scripts did not load.');
    return;
  }

  var addContainer = document.getElementById('add-form-container');
  var browseContainer = document.getElementById('word-list-container');
  var unitDatalist = document.getElementById('unit-options');
  if (!addContainer || !browseContainer || !unitDatalist) {
    console.error('[parent-app] mount containers / shared datalist missing from parent.html.');
    return;
  }

  var REQUEST_TIMEOUT_MS = 15000;

  /**
   * fetchJSON — small REST wrapper for the parent page.
   *   - Aborts via AbortController after REQUEST_TIMEOUT_MS.
   *   - Parses the JSON body; normalizes the {"error":{code,message}}
   *     envelope into a thrown Error carrying error.message (so toast copy
   *     can surface the backend message directly, Pitfall 2).
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
  // (all categories — both forms start with no selection, D-06) and on
  // every add-form category radio change. Both add forms share the ONE
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
  // Component wiring: browse list + tabbed add card, connected by the
  // refresh hook so a successful add updates the list without a reload.
  // ------------------------------------------------------------------
  var wordList = new window.WordList(browseContainer, {
    fetchWords: function () {
      return fetchJSON('GET', '/api/v1/words').then(function (data) {
        return (data && Array.isArray(data.words)) ? data.words : [];
      });
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
      wordList.refresh();
      refreshUnitOptions(selectedCategory);
    },
    onCategoryChange: function (category) {
      refreshUnitOptions(category);
    },
  });

  // Initial render: browse list + all-category unit suggestions.
  wordList.refresh();
  refreshUnitOptions('');

  // Debug exposure (app.js window.__app convention).
  window.__parentApp = {
    addCard: addCard,
    wordList: wordList,
    fetchJSON: fetchJSON,
    refreshUnitOptions: refreshUnitOptions,
  };
})();
