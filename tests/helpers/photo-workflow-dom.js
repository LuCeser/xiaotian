'use strict';

/**
 * Minimal DOM for tests/test-parent-photo-workflow.js (Issue #80 rethink
 * U2): just enough element behavior for PhotoAddForm's REAL render and
 * event paths — a child tree with textContent semantics, listeners +
 * fire(), an id registry behind document.getElementById, classList, and
 * a querySelector(All) that understands the single-token selectors
 * (#id / .class / tag) those paths use. Not a browser: anything outside
 * these paths should fail loudly rather than silently pass.
 */
function createPhotoDom() {
  var byId = {};
  var activeElement = null;

  function textOf(node) {
    if (!node) {
      return '';
    }
    var out = node._text || '';
    (node.childNodes || []).forEach(function (c) {
      out += textOf(c);
    });
    return out;
  }

  function matches(node, selector) {
    if (!node || node.nodeType !== 1) {
      return false;
    }
    if (selector.charAt(0) === '#') {
      return node.id === selector.slice(1);
    }
    if (selector.charAt(0) === '.') {
      return (' ' + node.className + ' ').indexOf(' ' + selector.slice(1) + ' ') >= 0;
    }
    return node.tagName === String(selector).toUpperCase();
  }

  function descendants(node, out) {
    (node.childNodes || []).forEach(function (c) {
      out.push(c);
      descendants(c, out);
    });
    return out;
  }

  function makeElement(tag) {
    var el = {
      tagName: String(tag || 'div').toUpperCase(),
      nodeType: 1,
      childNodes: [],
      parentNode: null,
      listeners: {},
      attrs: {},
      dataset: {},
      _text: '',
      // Form/preview state the production code reads and writes directly.
      value: '', checked: false, disabled: false, hidden: false,
      type: '', className: '', src: '', alt: '', title: '', placeholder: '',
      multiple: false, accept: '', noValidate: false, colSpan: 0, rows: 2,
    };

    // id rides a registry so readCreateForm's document.getElementById sees
    // the CURRENT panel inputs (a re-render's new input replaces the old).
    var id = '';
    Object.defineProperty(el, 'id', {
      configurable: true,
      get: function () { return id; },
      set: function (v) {
        if (id && byId[id] === el) {
          delete byId[id];
        }
        id = v == null ? '' : String(v);
        if (id) {
          byId[id] = el;
        }
      },
    });

    Object.defineProperty(el, 'firstChild', {
      configurable: true,
      get: function () { return el.childNodes[0] || null; },
    });

    // textContent setter clears the child tree (clearEl's fallback shape).
    Object.defineProperty(el, 'textContent', {
      configurable: true,
      get: function () { return textOf(el); },
      set: function (v) {
        el.childNodes.length = 0;
        el._text = v == null ? '' : String(v);
      },
    });

    el.appendChild = function (node) {
      if (node.parentNode) {
        node.parentNode.removeChild(node);
      }
      node.parentNode = el;
      el.childNodes.push(node);
      return node;
    };
    el.removeChild = function (node) {
      var i = el.childNodes.indexOf(node);
      if (i >= 0) {
        el.childNodes.splice(i, 1);
      }
      node.parentNode = null;
      return node;
    };
    el.addEventListener = function (type, fn) {
      (el.listeners[type] = el.listeners[type] || []).push(fn);
    };
    /** Dispatch one event through the registered listeners. */
    el.fire = function (type, event) {
      var ev = event || {};
      ev.type = type;
      if (!ev.preventDefault) {
        ev.preventDefault = function () {};
      }
      (el.listeners[type] || []).slice().forEach(function (fn) {
        fn.call(el, ev);
      });
    };
    el.focus = function () {
      activeElement = el;
    };
    el.setAttribute = function (key, value) {
      el.attrs[key] = String(value);
      if (key === 'id') {
        el.id = String(value);
      }
    };
    el.getAttribute = function (key) {
      return Object.prototype.hasOwnProperty.call(el.attrs, key) ? el.attrs[key] : null;
    };
    el.removeAttribute = function (key) {
      delete el.attrs[key];
    };
    el.classList = {
      add: function (c) {
        if (!el.classList.contains(c)) {
          el.className = (el.className ? el.className + ' ' : '') + c;
        }
      },
      remove: function (c) {
        el.className = el.className.split(/\s+/).filter(function (x) {
          return x && x !== c;
        }).join(' ');
      },
      contains: function (c) {
        return (' ' + el.className + ' ').indexOf(' ' + c + ' ') >= 0;
      },
      toggle: function (c, force) {
        var want = force === undefined ? !el.classList.contains(c) : !!force;
        if (want) {
          el.classList.add(c);
        } else {
          el.classList.remove(c);
        }
        return want;
      },
    };
    el.querySelector = function (selector) {
      var all = descendants(el, []);
      for (var i = 0; i < all.length; i++) {
        if (matches(all[i], selector)) {
          return all[i];
        }
      }
      return null;
    };
    el.querySelectorAll = function (selector) {
      return descendants(el, []).filter(function (n) {
        return matches(n, selector);
      });
    };
    return el;
  }

  return {
    document: {
      createElement: function (tag) {
        return makeElement(tag);
      },
      getElementById: function (wanted) {
        return Object.prototype.hasOwnProperty.call(byId, wanted) ? byId[wanted] : null;
      },
      get activeElement() {
        return activeElement;
      },
    },
  };
}

module.exports = { createPhotoDom: createPhotoDom };
