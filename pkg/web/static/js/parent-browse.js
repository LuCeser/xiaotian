/**
 * @file Parent page — browse-list component (Phase 9, Plan 01 tracer).
 *
 * WordList: the 浏览词库 word list. Fetches GET /api/v1/words and renders
 * flat word rows (word + meaning + category badge). Tracer scope is a flat
 * list — category pills, grouping/collapse/search arrive in Plans
 * 09-04/09-07 (component-internal change, no architecture impact).
 *
 * Security (Pitfall 10 / T-09-01): ALL data rendering goes through
 * document.createElement + Node.textContent — never innerHTML. Containers
 * clear via the firstChild-removeChild loop (Shared Pattern 4).
 */

(function (global) {
  'use strict';

  /** Clear a container via the firstChild-removeChild loop (Pattern 4). */
  function clearContainer(container) {
    while (container.firstChild) {
      container.removeChild(container.firstChild);
    }
  }

  function WordList(container, options) {
    options = options || {};
    this.fetchWords = options.fetchWords || function () {
      return Promise.reject(new Error('WordList: missing fetchWords handler'));
    };
    this.container = container;
    this.words = [];
  }

  /** Re-fetch from the backend and re-render (called after a successful add). */
  WordList.prototype.refresh = function () {
    var self = this;
    return this.fetchWords()
      .then(function (words) {
        self.words = Array.isArray(words) ? words : [];
        self.render();
      })
      .catch(function (err) {
        self.renderError(err);
      });
  };

  /** Render the load-failure banner with a 重试 button (UI-SPEC error state). */
  WordList.prototype.renderError = function (err) {
    var message = (err && err.message) ? err.message : '未知错误';
    clearContainer(this.container);

    var banner = document.createElement('p');
    banner.className = 'list-error';
    banner.textContent = '加载失败：' + message + '。点「重试」再试一次。';

    var retryBtn = document.createElement('button');
    retryBtn.type = 'button';
    retryBtn.className = 'btn btn-secondary';
    retryBtn.textContent = '重试';
    var self = this;
    retryBtn.addEventListener('click', function () {
      self.refresh();
    });

    this.container.appendChild(banner);
    this.container.appendChild(retryBtn);
  };

  WordList.prototype.render = function () {
    clearContainer(this.container);

    if (!this.words.length) {
      // Empty state per UI-SPEC Copywriting Contract.
      var empty = document.createElement('p');
      empty.className = 'list-empty';
      empty.textContent = '词库还是空的';
      this.container.appendChild(empty);
      return;
    }

    var list = document.createElement('div');
    list.className = 'word-list';
    this.words.forEach(function (word) {
      list.appendChild(WordList.buildRow(word));
    });
    this.container.appendChild(list);
  };

  /** Build one flat word row: word + meaning + category badge (+ unit). */
  WordList.buildRow = function (word) {
    var row = document.createElement('div');
    row.className = 'word-row';
    row.setAttribute('data-word', word.word);

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

    row.appendChild(text);
    if (word.meaning) {
      row.appendChild(meaning);
    }
    row.appendChild(badge);
    if (word.unit) {
      var unit = document.createElement('span');
      unit.className = 'word-unit';
      unit.textContent = word.unit;
      row.appendChild(unit);
    }
    return row;
  };

  // Window-or-module-global tail (state-manager.js convention) so Node
  // tests can require this file later.
  global.WordList = WordList;
})(typeof window !== 'undefined' ? window : this);
