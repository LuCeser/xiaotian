/**
 * @file rt-check-receipt — honest check-submit receipt copy for the tutor
 *       panel (Issue #96, U1). Pure logic: no DOM, runs in Node via require.
 */
(function (global) {
  'use strict';

  /**
   * @param {number} requested
   * @param {number} updated
   * @param {string[]} [missing]
   * @param {{ pending?: boolean }} [opts]
   * @returns {{ kind: 'saved'|'partial'|'failed'|'empty'|'pending', text: string }}
   */
  function formatCheckReceipt(requested, updated, missing, opts) {
    if (opts && opts.pending) {
      return { kind: 'pending', text: '批改结果已保留。复习安排待核实。' };
    }
    var req = requested | 0;
    var upd = updated | 0;
    if (req <= 0) {
      return { kind: 'empty', text: '这次没有要保存的学习记录。' };
    }
    if (upd <= 0) {
      return { kind: 'failed', text: '批改结果已保留。复习安排没有保存。' };
    }
    if (upd < req) {
      var rest = req - upd;
      return {
        kind: 'partial',
        text: '批改已保存。复习安排更新了 ' + upd + ' 个，还有 ' + rest + ' 个没有更新。'
      };
    }
    return {
      kind: 'saved',
      text: '批改已保存。复习安排已更新，' + req + ' 个词。'
    };
  }

  global.formatCheckReceipt = formatCheckReceipt;
})(typeof window !== 'undefined' ? window : this);
