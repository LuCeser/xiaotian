/**
 * @file index-banner — degraded-mode banner for the legacy button-driven
 *       dictation page (Phase 13, Plan 05, RT-10).
 *
 * The legacy "/" page is now the explicitly-labeled FALLBACK (13-CONTEXT
 * decision): its push-to-talk interaction does NOT count as voice
 * acceptance. This banner says so in plain Chinese and links to the
 * natural real-time tutor at /tutor/.
 *
 * Minimal intrusion: loaded with `defer`, depends on nothing (no app.js
 * changes, no shared styles), inserts itself as the first child of <body>
 * on DOMContentLoaded (or immediately when the document is already parsed).
 */
(function (global) {
  'use strict';

  var BANNER_ID = 'rt-degraded-banner';

  function install() {
    var doc = global.document;
    if (!doc || !doc.body || doc.getElementById(BANNER_ID)) return;

    var banner = doc.createElement('div');
    banner.id = BANNER_ID;
    banner.setAttribute('role', 'status');
    banner.textContent = '此页面为降级备用模式，不计语音验收；自然实时家教请进入 ';

    var link = doc.createElement('a');
    link.href = '/tutor/';
    link.textContent = '/tutor/';
    link.style.cssText = 'color:#1d5fd0;font-weight:700;text-decoration:underline;';
    banner.appendChild(link);

    banner.style.cssText =
      'display:block;box-sizing:border-box;width:100%;padding:12px 16px;' +
      'background:#fff7e0;border-bottom:2px solid #e8c96a;color:#7a5c14;' +
      'font-size:16px;line-height:1.5;text-align:center;';

    doc.body.insertBefore(banner, doc.body.firstChild);
  }

  if (typeof global.document !== 'undefined') {
    if (global.document.readyState === 'loading') {
      global.document.addEventListener('DOMContentLoaded', install);
    } else {
      install();
    }
  }

  global.RTDegradedBanner = { install: install };
})(typeof window !== 'undefined' ? window : this);
