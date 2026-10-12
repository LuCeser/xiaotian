/**
 * @file Parent page — 教材管理 (Issue #4): textbook entities, batch
 *       lesson attribution and learned progress. Two modal surfaces:
 *
 *   TextbookUI.openManager(category) — the 教材管理 modal: list/create/
 *       edit textbooks, set 已学完第 N 课 progress, set 主教材.
 *   TextbookUI.openAssign(cardIds, words) — the 批量指定 modal: give the
 *       selected words a textbook + lesson (or 待确认), or the parent's
 *       explicit include-in-scope override.
 *
 * Scope semantics (the modal copy states it): 已学完第 N 课 INCLUDES
 * lesson N; later lessons stay out of automatic selection until the
 * progress moves; 待确认 (empty lesson) is never treated as learned.
 * Supplementary words (no textbook) keep issue #2's participation switch
 * as their only gate.
 *
 * parseLessonsText / textbookMetaSummary / progressLabel are PURE and
 * exposed on the window for the Node unit test
 * (tests/test-parent-textbook-form.js — window.parseBatch precedent).
 *
 * Modals ride window.__parentModalKit (openModal/buildModalField exported
 * by parent-browse.js) — accessed at INTERACTION time, after every script
 * has loaded. Rendering is textContent-only (Pitfall 10).
 */

(function (global) {
  'use strict';

  /** Field caps mirroring pkg/web/api validate.go. */
  var MAX_TEXTBOOK_FIELD = 100;
  var MAX_TEXTBOOK_LESSONS = 200;
  var MAX_TEXTBOOK_LESSON_RUNES = 100;

  /**
   * parseLessonsText — the 课次顺序 textarea → ordered lesson array.
   * One lesson per line; line order IS the teaching order (never sorted).
   * Pure: returns {lessons, error}.
   */
  function parseLessonsText(text) {
    var lines = String(text == null ? '' : text).split(/\r?\n/);
    var lessons = [];
    var seen = {};
    for (var i = 0; i < lines.length; i++) {
      var name = lines[i].trim();
      if (!name) {
        continue;
      }
      if (name.length > MAX_TEXTBOOK_LESSON_RUNES) {
        return { lessons: [], error: '课次名称不能超过 ' + MAX_TEXTBOOK_LESSON_RUNES + ' 个字符（第 ' + (i + 1) + ' 行）' };
      }
      if (seen[name]) {
        return { lessons: [], error: '课次「' + name + '」重复了，同一本教材内课次名称不能重复' };
      }
      seen[name] = true;
      lessons.push(name);
    }
    if (lessons.length > MAX_TEXTBOOK_LESSONS) {
      return { lessons: [], error: '课次最多 ' + MAX_TEXTBOOK_LESSONS + ' 个' };
    }
    return { lessons: lessons, error: '' };
  }

  /** lessonsText — the inverse of parseLessonsText (round-trip for edit). */
  function lessonsText(lessons) {
    return (lessons || []).join('\n');
  }

  /**
   * textbookMetaSummary — the metadata fields → one display string
   * (版本·学段·年级·上下册·模块, skipping empties). Pure.
   */
  function textbookMetaSummary(book) {
    var parts = [];
    ['edition', 'stage', 'grade', 'volume', 'module'].forEach(function (key) {
      var v = book && book[key] ? String(book[key]).trim() : '';
      if (v) {
        parts.push(v);
      }
    });
    return parts.join('·');
  }

  /**
   * progressLabel — the progress line for one textbook. Pure.
   * No lessons yet → a hint instead of a number.
   */
  function progressLabel(book) {
    var total = book && book.lessons ? book.lessons.length : 0;
    var learned = book && book.learnedLessons ? book.learnedLessons : 0;
    if (!total) {
      return '还没有课次表';
    }
    if (!learned) {
      return '尚未开始（共 ' + total + ' 课）';
    }
    var name = learned <= total ? book.lessons[learned - 1] : '';
    return '已学完第 ' + learned + ' 课「' + name + '」（共 ' + total + ' 课）';
  }

  /** assignLabel — one word's attribution chip text. */
  function assignLabel(entry) {
    var label = entry.name + (entry.lesson ? '·' + entry.lesson : '·待确认');
    if (entry.includeInScope) {
      label += '（已加入范围）';
    }
    return label;
  }

  // ------------------------------------------------------------------
  // Wired callbacks — set by TextbookUI.init from parent-app.js.
  // ------------------------------------------------------------------

  var deps = {
    fetchTextbooks: function () { return Promise.resolve([]); },
    createTextbook: function () { return Promise.reject(new Error('未配置')); },
    updateTextbook: function () { return Promise.reject(new Error('未配置')); },
    assignWords: function () { return Promise.reject(new Error('未配置')); },
    onMutated: function () {},
  };

  /** modalKit resolves the shared modal builders at interaction time. */
  function modalKit() {
    var kit = global.__parentModalKit;
    if (!kit || typeof kit.openModal !== 'function') {
      return null;
    }
    return kit;
  }

  function toast(message, kind) {
    var kit = global.__parentFormKit;
    if (kit && kit.showToast) {
      kit.showToast(message, kind);
    }
  }

  function setLoading(btn, busy) {
    var kit = global.__parentFormKit;
    if (kit && kit.setLoading) {
      kit.setLoading(btn, busy);
    } else {
      btn.disabled = busy;
    }
  }

  /** Field builder on top of the modal kit (null-checks the kit). */
  function modalField(labelText, id, isTextarea) {
    var kit = modalKit();
    return kit.buildModalField(labelText, id, isTextarea);
  }

  // ------------------------------------------------------------------
  // 教材管理 modal
  // ------------------------------------------------------------------

  /**
   * openManager(categoryHint) — the textbook management modal. category
   * seeds the create form's category select (the browse pills' current
   * value when opened from the 词库 toolbar).
   */
  function openManager(categoryHint) {
    var kit = modalKit();
    if (!kit) {
      return;
    }
    kit.openModal({
      title: '教材管理',
      wide: true,
      build: function (body, actions, close) {
        var listHost = document.createElement('div');
        listHost.className = 'textbook-list';
        body.appendChild(listHost);

        var hint = document.createElement('p');
        hint.className = 'form-hint';
        hint.textContent = '每本教材单独记录已学进度；已学完第 N 课包含第 N 课，之后的课次不会自动选入默写。进入默写范围表示可以被安排，不代表下一课练全部内容。';
        body.appendChild(hint);

        renderManagerList(listHost, close);

        var form = document.createElement('form');
        form.className = 'textbook-create-form';
        form.noValidate = true;

        var title = document.createElement('h4');
        title.textContent = '新建教材';
        form.appendChild(title);

        var nameField = modalField('教材名称（必填）', 'tb-name');
        form.appendChild(nameField.field);

        var catField = document.createElement('div');
        catField.className = 'form-field';
        var catLabel = document.createElement('span');
        catLabel.className = 'form-label';
        catLabel.textContent = '学科';
        var catSelect = document.createElement('select');
        catSelect.className = 'form-input';
        catSelect.id = 'tb-category';
        [
          { value: 'english', text: '英语' },
          { value: 'chinese', text: '语文' },
        ].forEach(function (opt) {
          var o = document.createElement('option');
          o.value = opt.value;
          o.textContent = opt.text;
          catSelect.appendChild(o);
        });
        if (categoryHint === 'chinese') {
          catSelect.value = 'chinese';
        }
        catField.appendChild(catLabel);
        catField.appendChild(catSelect);
        form.appendChild(catField);

        // The five optional metadata fields, one compact row each.
        [
          { key: 'edition', label: '版本（选填）', ph: '如 人教版' },
          { key: 'stage', label: '学段（选填）', ph: '如 小学' },
          { key: 'grade', label: '年级（选填）', ph: '如 三年级' },
          { key: 'volume', label: '上下册（选填）', ph: '如 上册' },
          { key: 'module', label: '模块（选填）', ph: '如 Unit 1-4' },
        ].forEach(function (meta) {
          var field = modalField(meta.label, 'tb-' + meta.key);
          field.input.placeholder = meta.ph;
          form.appendChild(field.field);
        });

        var lessonsField = modalField('课次顺序（选填，每行一个，行序即教学顺序）', 'tb-lessons', true);
        form.appendChild(lessonsField.field);

        var formError = document.createElement('p');
        formError.className = 'form-error';
        formError.setAttribute('aria-live', 'polite');
        formError.hidden = true;
        form.appendChild(formError);

        var submit = document.createElement('button');
        submit.type = 'submit';
        submit.className = 'btn btn-primary';
        submit.textContent = '创建教材';
        actions.appendChild(submit);

        var cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'btn btn-secondary';
        cancel.textContent = '关闭';
        cancel.addEventListener('click', close);
        actions.appendChild(cancel);

        form.addEventListener('submit', function (event) {
          event.preventDefault();
          formError.hidden = true;
          var name = nameField.input.value.trim();
          if (!name || name.length > MAX_TEXTBOOK_FIELD) {
            formError.textContent = '教材名称必填，且不超过 ' + MAX_TEXTBOOK_FIELD + ' 个字符';
            formError.hidden = false;
            return;
          }
          var parsed = parseLessonsText(lessonsField.input.value);
          if (parsed.error) {
            formError.textContent = parsed.error;
            formError.hidden = false;
            return;
          }
          var payload = { name: name, category: catSelect.value, lessons: parsed.lessons };
          ['edition', 'stage', 'grade', 'volume', 'module'].forEach(function (key) {
            var v = document.getElementById('tb-' + key);
            if (v && v.value.trim()) {
              payload[key] = v.value.trim();
            }
          });
          setLoading(submit, true);
          deps.createTextbook(payload).then(function () {
            toast('教材已创建', 'success');
            nameField.input.value = '';
            lessonsField.input.value = '';
            renderManagerList(listHost, close);
            deps.onMutated();
          }).catch(function (err) {
            formError.textContent = '创建失败：' + ((err && err.message) || '未知错误');
            formError.hidden = false;
          }).then(function () {
            setLoading(submit, false);
          });
        });
        body.appendChild(form);
      },
    });
  }

  /** The textbook list inside the manager modal. */
  function renderManagerList(host, close) {
    while (host.firstChild) {
      host.removeChild(host.firstChild);
    }
    var note = document.createElement('p');
    note.className = 'form-hint';
    note.textContent = '读取中…';
    host.appendChild(note);

    deps.fetchTextbooks().then(function (books) {
      while (host.firstChild) {
        host.removeChild(host.firstChild);
      }
      if (!books || !books.length) {
        var empty = document.createElement('p');
        empty.className = 'form-hint';
        empty.textContent = '还没有教材。在下方新建一本，然后在词库勾选单词批量指定课次。';
        host.appendChild(empty);
        return;
      }
      books.forEach(function (book) {
        host.appendChild(managerRow(book, close, function () {
          renderManagerList(host, close);
        }));
      });
    }).catch(function () {
      note.textContent = '教材读取失败，请重试';
    });
  }

  /** One textbook row: name/meta/primary badge, progress, actions. */
  function managerRow(book, close, rerender) {
    var row = document.createElement('div');
    row.className = 'textbook-row';
    row.setAttribute('data-textbook-id', String(book.id));

    var head = document.createElement('div');
    head.className = 'textbook-row-head';
    var name = document.createElement('strong');
    name.className = 'textbook-name';
    name.textContent = book.name;
    head.appendChild(name);
    if (book.isPrimary) {
      var primary = document.createElement('span');
      primary.className = 'textbook-primary-badge';
      primary.textContent = '主教材';
      head.appendChild(primary);
    }
    var meta = document.createElement('span');
    meta.className = 'textbook-meta';
    var metaText = textbookMetaSummary(book);
    if (metaText) {
      meta.textContent = metaText;
      head.appendChild(meta);
    }
    var count = document.createElement('span');
    count.className = 'textbook-meta';
    count.textContent = book.assignmentCount + ' 个词';
    head.appendChild(count);
    row.appendChild(head);

    var progress = document.createElement('p');
    progress.className = 'textbook-progress';
    progress.textContent = progressLabel(book);
    row.appendChild(progress);

    var controls = document.createElement('div');
    controls.className = 'textbook-controls';

    // Progress select: 尚未开始 + 已学完第 1..N 课.
    var progressSelect = document.createElement('select');
    progressSelect.className = 'form-input textbook-progress-select';
    progressSelect.setAttribute('aria-label', '已学进度 ' + book.name);
    var total = book.lessons.length;
    for (var n = 0; n <= total; n++) {
      var opt = document.createElement('option');
      opt.value = String(n);
      opt.textContent = n === 0
        ? '尚未开始'
        : '已学完第 ' + n + ' 课「' + book.lessons[n - 1] + '」';
      opt.selected = book.learnedLessons === n;
      progressSelect.appendChild(opt);
    }
    progressSelect.disabled = total === 0;
    progressSelect.addEventListener('change', function () {
      var n = parseInt(progressSelect.value, 10) || 0;
      setLoading(progressSelect, true);
      deps.updateTextbook(book.id, { learnedLessons: n }).then(function () {
        toast('已更新：' + progressLabel({ lessons: book.lessons, learnedLessons: n }), 'success');
        deps.onMutated();
        rerender();
      }).catch(function (err) {
        toast('进度更新失败：' + ((err && err.message) || '未知错误'), 'error');
        progressSelect.value = String(book.learnedLessons);
      }).then(function () {
        progressSelect.disabled = total === 0;
      });
    });
    controls.appendChild(progressSelect);

    // Lesson list editor.
    var lessonsBtn = document.createElement('button');
    lessonsBtn.type = 'button';
    lessonsBtn.className = 'btn btn-secondary';
    lessonsBtn.textContent = '编辑课次';
    lessonsBtn.addEventListener('click', function () {
      openLessonsEditor(book, rerender);
    });
    controls.appendChild(lessonsBtn);

    // Primary designation.
    if (!book.isPrimary) {
      var primaryBtn = document.createElement('button');
      primaryBtn.type = 'button';
      primaryBtn.className = 'btn btn-secondary';
      primaryBtn.textContent = '设为主教材';
      primaryBtn.addEventListener('click', function () {
        setLoading(primaryBtn, true);
        deps.updateTextbook(book.id, { setPrimary: true }).then(function () {
          toast('已将「' + book.name + '」设为主教材', 'success');
          deps.onMutated();
          rerender();
        }).catch(function (err) {
          toast('设置失败：' + ((err && err.message) || '未知错误'), 'error');
        }).then(function () {
          setLoading(primaryBtn, false);
        });
      });
      controls.appendChild(primaryBtn);
    }

    row.appendChild(controls);
    return row;
  }

  /** The lesson-list editor modal (order-preserving textarea). */
  function openLessonsEditor(book, rerender) {
    var kit = modalKit();
    if (!kit) {
      return;
    }
    kit.openModal({
      title: '编辑课次 — ' + book.name,
      build: function (body, actions, close) {
        var hint = document.createElement('p');
        hint.className = 'form-hint';
        hint.textContent = '每行一个课次，行序即教学顺序（不按名称排序）。已有归属会保留；改名后旧课次名不再算已学，可重新批量指定。';
        body.appendChild(hint);

        var field = modalField('课次列表', 'tb-lessons-edit', true);
        field.input.value = lessonsText(book.lessons);
        body.appendChild(field.field);

        var error = document.createElement('p');
        error.className = 'form-error';
        error.hidden = true;
        body.appendChild(error);

        var save = document.createElement('button');
        save.type = 'button';
        save.className = 'btn btn-primary';
        save.textContent = '保存课次';
        save.addEventListener('click', function () {
          error.hidden = true;
          var parsed = parseLessonsText(field.input.value);
          if (parsed.error) {
            error.textContent = parsed.error;
            error.hidden = false;
            return;
          }
          setLoading(save, true);
          deps.updateTextbook(book.id, { lessons: parsed.lessons }).then(function () {
            toast('课次已保存', 'success');
            deps.onMutated();
            rerender();
            close();
          }).catch(function (err) {
            error.textContent = '保存失败：' + ((err && err.message) || '未知错误');
            error.hidden = false;
          }).then(function () {
            setLoading(save, false);
          });
        });
        actions.appendChild(save);

        var cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'btn btn-secondary';
        cancel.textContent = '取消';
        cancel.addEventListener('click', close);
        actions.appendChild(cancel);
      },
    });
  }

  // ------------------------------------------------------------------
  // 批量指定 modal
  // ------------------------------------------------------------------

  /**
   * openAssign(cardIds, words) — assign the selected words to a textbook
   * lesson. words carry {id, category} so the textbook select can filter
   * by the batch's (single) category; a mixed batch asks the parent to
   * filter first (the server would reject the mismatched ones anyway).
   */
  function openAssign(cardIds, words) {
    var kit = modalKit();
    if (!kit) {
      return;
    }
    var ids = (cardIds || []).slice(0, 500);
    var categories = {};
    (words || []).forEach(function (w) {
      if (w && w.category) {
        categories[w.category] = true;
      }
    });
    var category = Object.keys(categories).length === 1 ? Object.keys(categories)[0] : '';

    kit.openModal({
      title: '指定教材课次（' + ids.length + ' 个词）',
      build: function (body, actions, close) {
        var error = document.createElement('p');
        error.className = 'form-error';
        error.setAttribute('aria-live', 'polite');
        error.hidden = true;
        body.appendChild(error);

        if (!category) {
          error.textContent = '所选词条包含多个学科，请先用筛选只选英语或语文的词';
          error.hidden = false;
        }

        var booksNote = document.createElement('p');
        booksNote.className = 'form-hint';
        booksNote.textContent = category ? '读取教材中…' : '';
        body.appendChild(booksNote);

        var bookField = modalField('教材', 'assign-textbook');
        var bookSelect = bookField.input;
        // buildModalField returns a text input; swap it for a select.
        var bookSelectEl = document.createElement('select');
        bookSelectEl.className = 'form-input';
        bookSelectEl.id = 'assign-textbook';
        bookSelectEl.setAttribute('aria-label', '教材');
        bookField.field.replaceChild(bookSelectEl, bookSelect);
        body.appendChild(bookField.field);

        var lessonField = modalField('课次', 'assign-lesson');
        var lessonInput = lessonField.input;
        var lessonSelect = document.createElement('select');
        lessonSelect.className = 'form-input';
        lessonSelect.id = 'assign-lesson';
        lessonSelect.setAttribute('aria-label', '课次');
        lessonField.field.replaceChild(lessonSelect, lessonInput);
        body.appendChild(lessonField.field);

        var includeLabel = document.createElement('label');
        includeLabel.className = 'textbook-include-toggle';
        var include = document.createElement('input');
        include.type = 'checkbox';
        include.id = 'assign-include';
        var includeText = document.createElement('span');
        includeText.textContent = '不受课次进度限制，直接加入默写范围';
        includeLabel.appendChild(include);
        includeLabel.appendChild(includeText);
        body.appendChild(includeLabel);

        var scopeHint = document.createElement('p');
        scopeHint.className = 'form-hint';
        scopeHint.textContent = '选「待确认」的词先不参与自动默写，等确认课次或勾选加入范围。';
        body.appendChild(scopeHint);

        var submit = document.createElement('button');
        submit.type = 'button';
        submit.className = 'btn btn-primary';
        submit.textContent = '确定指定';
        submit.disabled = true;
        submit.addEventListener('click', function () {
          var bookId = parseInt(bookSelectEl.value, 10);
          if (!bookId) {
            return;
          }
          error.hidden = true;
          setLoading(submit, true);
          deps.assignWords(bookId, {
            cardIds: ids,
            lesson: lessonSelect.value,
            includeInScope: include.checked,
          }).then(function () {
            toast('已指定 ' + ids.length + ' 个词', 'success');
            deps.onMutated();
            close();
          }).catch(function (err) {
            error.textContent = '指定失败：' + ((err && err.message) || '未知错误');
            error.hidden = false;
          }).then(function () {
            setLoading(submit, false);
          });
        });
        actions.appendChild(submit);

        var cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'btn btn-secondary';
        cancel.textContent = '取消';
        cancel.addEventListener('click', close);
        actions.appendChild(cancel);

        if (!category) {
          return; // mixed batch: nothing to load
        }

        deps.fetchTextbooks(category).then(function (books) {
          booksNote.textContent = '';
          if (!books || !books.length) {
            booksNote.textContent = '该学科还没有教材，请先在「教材管理」里创建。';
            return;
          }
          books.forEach(function (book) {
            var opt = document.createElement('option');
            opt.value = String(book.id);
            opt.textContent = book.name + (textbookMetaSummary(book) ? '（' + textbookMetaSummary(book) + '）' : '');
            bookSelectEl.appendChild(opt);
          });
          fillLessonOptions(lessonSelect, books[0]);
          submit.disabled = false;
          bookSelectEl.addEventListener('change', function () {
            var id = parseInt(bookSelectEl.value, 10);
            var found = null;
            books.forEach(function (b) {
              if (b.id === id) {
                found = b;
              }
            });
            fillLessonOptions(lessonSelect, found);
          });
        }).catch(function () {
          booksNote.textContent = '教材读取失败，请关闭后重试';
        });
      },
    });
  }

  /** Lesson options for the assign modal: 待确认 first, then the list. */
  function fillLessonOptions(select, book) {
    while (select.firstChild) {
      select.removeChild(select.firstChild);
    }
    var pending = document.createElement('option');
    pending.value = '';
    pending.textContent = '待确认（课次不确定）';
    select.appendChild(pending);
    if (!book) {
      return;
    }
    (book.lessons || []).forEach(function (lesson) {
      var opt = document.createElement('option');
      opt.value = lesson;
      opt.textContent = lesson;
      select.appendChild(opt);
    });
  }

  // ------------------------------------------------------------------
  // Module surface
  // ------------------------------------------------------------------

  global.TextbookUI = {
    init: function (wiring) {
      deps.fetchTextbooks = wiring.fetchTextbooks || deps.fetchTextbooks;
      deps.createTextbook = wiring.createTextbook || deps.createTextbook;
      deps.updateTextbook = wiring.updateTextbook || deps.updateTextbook;
      deps.assignWords = wiring.assignWords || deps.assignWords;
      deps.onMutated = wiring.onMutated || deps.onMutated;
    },
    openManager: openManager,
    openAssign: openAssign,
  };
  global.parseLessonsText = parseLessonsText;
  global.lessonsText = lessonsText;
  global.textbookMetaSummary = textbookMetaSummary;
  global.progressLabel = progressLabel;
  global.assignLabel = assignLabel;
})(typeof window !== 'undefined' ? window : this);
