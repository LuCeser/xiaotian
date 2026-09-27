/**
 * @file Parent Vocabulary Management — application entry point
 *       (Phase 9, Plan 01 tracer).
 *
 * Loaded with `defer` from parent.html after parent-modes.js and
 * parent-browse.js so both constructors are available (mirrors app.js
 * IIFE + window.X availability-check pattern).
 *
 * Responsibilities:
 *   1. Provide the fetchJSON wrapper: AbortController timeout +
 *      normalization of the {"error":{code,message}} envelope into a
 *      thrown Error carrying the message.
 *   2. Mount WordList into #word-list-container (浏览词库).
 *   3. Mount SingleAddForm into #add-form-container (添加单词).
 *   4. After a successful add, call the browse refresh hook so the new
 *      word appears without a page reload.
 */

(function parentAppEntry() {
  'use strict';

  // ------------------------------------------------------------------
  // Dependency availability checks (graceful degradation, app.js pattern)
  // ------------------------------------------------------------------
  if (!window.SingleAddForm || !window.WordList) {
    console.error('[parent-app] SingleAddForm/WordList unavailable — component scripts did not load.');
    return;
  }

  var addContainer = document.getElementById('add-form-container');
  var browseContainer = document.getElementById('word-list-container');
  if (!addContainer || !browseContainer) {
    console.error('[parent-app] mount containers missing from parent.html.');
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

  // ------------------------------------------------------------------
  // Component wiring: browse list + add form, connected by the refresh
  // hook so a successful add updates the list without a page reload.
  // ------------------------------------------------------------------
  var wordList = new window.WordList(browseContainer, {
    fetchWords: function () {
      return fetchJSON('GET', '/api/v1/words').then(function (data) {
        return (data && Array.isArray(data.words)) ? data.words : [];
      });
    },
  });

  var addForm = new window.SingleAddForm(addContainer, {
    onSubmit: function (payload) {
      return fetchJSON('POST', '/api/v1/words', payload);
    },
    onSuccess: function () {
      wordList.refresh();
    },
  });

  // Initial render of the browse list.
  wordList.refresh();

  // Debug exposure (app.js window.__app convention).
  window.__parentApp = {
    addForm: addForm,
    wordList: wordList,
    fetchJSON: fetchJSON,
  };
})();
