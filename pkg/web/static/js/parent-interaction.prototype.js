/* THROWAWAY: three parent-page variants, ?variant=A|B|C. All writes stay in memory.
   Question: which sequence makes photo review, progress confirmation and bulk editing understandable?
   Palette and type follow parent.css. A=parallel workspace, B=conversation, C=textbook notebook.
   The 39 word/meaning pairs are read-only sample content; lesson assignments and AI/OCR are simulated. */
(() => {
  'use strict';
  if (document.documentElement.dataset.interactionPrototype !== 'true') return;
  const $ = (s, r = document) => r.querySelector(s);
  const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const source = [['library','图书馆'],['classroom','教室'],['hall','礼堂'],['study','学习'],['tidy','整洁的'],['project','课题'],['begin','开始'],['let','允许；让'],['history','历史'],['bamboo','竹子'],['special','特别的'],['student','学生'],['learn','学习'],['tall','高大的'],['umbrella','伞'],['interesting','有趣的'],['each','每一个'],['boat','船'],['chair','椅子'],['toilet','厕所'],['desk','课桌'],['favourite','最喜欢的'],['try','试一试'],['school building','教学楼'],['computer room','计算机房'],['sports field','操场'],['art room','美术室'],['on the sports field','在运动场上'],['do sports','做运动'],['keep...tidy','保持……整洁'],['in groups','小组形式'],['look for','寻找'],['on the ground floor','在一楼；底楼'],['on the second floor','在三楼（英式楼层）'],['be made of','由……制成'],['music room','音乐教室'],['learn and play','学和玩'],['look like','看起来像……'],['my favourite place in the school','学校里我最喜欢的地方']];
  const fresh = [['garden','花园'],['river','河流'],['mountain','山'],['forest','森林'],['bridge','桥'],['village','村庄'],['lake','湖'],['tree','树']];
  const names = { A: '并排核对', B: '对话引导', C: '按教材整理' };
  const scenes = { first: '首次导入，尚未告诉进度', known: '导入已学单元，不重复确认', new: '导入第四单元，已学到第三单元', edit: '词库中的 39 个词，批量编辑' };
  let variant = new URL(location.href).searchParams.get('variant') || 'A';
  if (!names[variant]) variant = 'A';
  let state, photoURL, toastTimer, modalReturnFocus;
  const root = document.createElement('div'); root.className = 'p-root';
  $('#vocabPanel').replaceChildren(root);
  const button = (label, action, extra = '', cls = '') => `<button type="button" class="p-btn ${cls}" data-action="${action}" ${extra}>${label}</button>`;
  const tag = (text, kind = '') => `<span class="p-tag ${kind}">${esc(text)}</span>`;
  const progress = n => n ? `第 ${n} 单元` : '尚未告诉小天';
  const eligible = w => w.participates && (w.manual || (state.progress > 0 && w.unit <= state.progress));
  function reset(scene = 'new') {
    if (photoURL) URL.revokeObjectURL(photoURL); photoURL = null;
    state = { scene, progress: scene === 'first' ? 0 : 3, target: scene === 'first' ? 1 : scene === 'known' ? 2 : 4,
      words: scene === 'first' ? [] : source.map(([word, meaning], i) => ({ id: i + 1, word, meaning, unit: Math.floor(i / 13) + 1, participates: true, manual: false, due: i % 3 === 0 })),
      draft: [], selected: [], view: scene === 'edit' ? 'library' : 'draft', choice: null, proposed: null,
      filter: 0, query: '', edit: false, backup: null, receipt: null, messages: [], events: [], reviewUpdates: 0, fileName: '英语词表.jpg' };
    if (scene !== 'edit') {
      const data = scene === 'first' ? source.slice(0, 13) : fresh;
      state.draft = data.map(([word, meaning], i) => ({ id: 100 + i, word: i === 0 && scene !== 'first' ? 'gardcn' : word, meaning, unit: state.target, participates: true, manual: false, due: true, suspect: i === 0 && scene !== 'first' }));
      state.selected = state.draft.map(w => w.id);
    }
    log('载入演示场景'); render();
  }
  function log(text) { state.events.push(text); }
  function shown() { const q = state.query.toLowerCase(); return (state.view === 'draft' ? state.draft : state.words).filter(w => (!state.filter || w.unit === state.filter) && (!q || `${w.word} ${w.meaning}`.toLowerCase().includes(q))); }
  function selected() { return (state.view === 'draft' ? state.draft : state.words).filter(w => state.selected.includes(w.id)); }
  function showToast(text) {
    $('.p-toast')?.remove(); clearTimeout(toastTimer);
    const el = document.createElement('div'); el.className = 'p-toast'; el.setAttribute('role', 'status'); el.textContent = text; document.body.append(el); toastTimer = setTimeout(() => el.remove(), 4000);
  }
  function reviewBar() { return `<div class="p-review"><strong>交互原型 · 示例识别与回复 · 刷新后重置</strong><label class="visually-hidden" for="p-scene">演示场景</label><select id="p-scene">${Object.entries(scenes).map(([key, label]) => `<option value="${key}" ${key === state.scene ? 'selected' : ''}>${label}</option>`).join('')}</select><button data-action="reset">重置场景</button><details class="p-review-note"><summary>原型说明</summary>照片只在本机预览，识别结果和大模型回复使用固定示例，不调用服务。39 个词面取自现有词库；教材、课次、到期时间为演示数据。未连接真实保存、语音或复习算法。底部箭头切换三个方案，操作状态会保留。</details></div>`; }
  function stateLine() { return `<div class="p-state-line"><span>教材 <b>小学英语 · 四年级上册</b></span><span>已学到 <b>${progress(state.progress)}</b></span><span>已保存 <b>${state.words.length} 词</b></span><span>可安排默写 <b>${state.words.filter(eligible).length} 词</b></span></div>`; }
  function sourcePanel() { return `<div class="p-stack p-source-stack"><div><h3 class="p-source-label">本次材料</h3><div class="p-photo">${photoURL ? `<img src="${photoURL}" alt="本次选择的词表图片，仅本地预览">` : `<div class="p-sheet"><h4>Unit ${state.target} Vocabulary</h4>${(state.draft.length ? state.draft : state.words.slice(0, 6)).slice(0, 7).map(w => `<p><span>${esc(w.word === 'gardcn' ? 'garden' : w.word)}</span><span>${esc(w.meaning)}</span></p>`).join('')}<p>英语词表 · 示例材料</p></div>`}</div><div class="p-upload"><label class="p-btn p-btn-small" for="p-upload">选择照片</label><input id="p-upload" type="file" accept="image/*"><p class="p-small" style="margin-top:10px">使用示例识别结果，不上传照片</p></div></div></div>`; }
  function question() { if (!state.progress) return '这本书现在学到哪里了？'; return `这次识别到第 ${state.target} 单元，你之前告诉我学到第 ${state.progress} 单元。要更新进度，还是只让这批词参与默写？`; }
  function choices(horizontal = false) {
    if (state.view !== 'draft') return '';
    if (state.target <= state.progress) return `<div class="p-notice p-notice-good">第 ${state.target} 单元已经学到，这批词保存后可以安排默写，无需重新确认进度。</div>`;
    const options = [['advance', `学到第 ${state.target} 单元了`, `第 1～${state.target} 单元都可以安排默写`], ['include', '只把这批词加入默写', `教学进度${state.progress ? `仍是第 ${state.progress} 单元` : '暂不设置'}，这批词持续参与`], ['keep', '先保存这些词', '保留教学进度，暂不加入自动默写']];
    return `<div class="p-choices ${horizontal ? 'p-horizontal' : ''}">${options.map(([id, title, note]) => `<button class="p-choice" data-action="choice" data-choice="${id}" aria-pressed="${state.choice === id}"><strong>${esc(title)}</strong><span>${esc(note)}</span></button>`).join('')}</div>${state.choice === 'advance' && state.proposed && state.proposed !== state.target ? `<p class="p-small">已按你的说明，保存时将进度记为第 ${state.proposed} 单元。</p>` : ''}`;
  }
  function command() { return `<form class="p-command"><input aria-label="告诉小天你的要求" name="instruction" placeholder="例如：现在学到第三单元了" autocomplete="off"><button class="p-btn" type="submit">告诉小天</button></form>`; }
  function coach(horizontal = false) { return `<section class="p-coach"><h3>${state.view === 'draft' ? state.target > state.progress ? '确认学习安排' : '学习安排' : '告诉小天'}</h3><p class="p-muted">${state.view === 'draft' ? state.target > state.progress ? question() : `记住的进度：${progress(state.progress)}` : '可以更新教学进度，也可以说“今天默写第三单元”。'}</p>${choices(horizontal)}${command()}${state.messages.length ? `<div class="p-notice">${esc(state.messages.at(-1))}</div>` : ''}</section>`; }
  function tabs() { return `<div class="p-tabs" role="tablist" aria-label="词库内容"><button data-action="view" data-view="draft" role="tab" aria-selected="${state.view === 'draft'}">本次导入 ${state.draft.length ? `(${state.draft.length})` : ''}</button><button data-action="view" data-view="library" role="tab" aria-selected="${state.view === 'library'}">词库 (${state.words.length})</button></div>`; }
  function toolbar() { const edit = state.view === 'library'; return `<div class="p-toolbar"><span class="p-selected">已选 ${selected().length} 个</span>${button('批量粘贴编辑', 'paste', '', 'p-btn-small')}${button('指定课次', 'assign', '', 'p-btn-small')}${edit ? button(state.edit ? '保存修改' : '连续编辑', state.edit ? 'save-edits' : 'edit', '', 'p-btn-small') + (state.edit ? button('取消', 'cancel-edits', '', 'p-btn-small') : button('加入默写', 'include-selected', '', 'p-btn-small') + button('移出默写', 'exclude-selected', '', 'p-btn-small')) : ''}<input class="p-search" type="search" aria-label="搜索词条" placeholder="搜索词面或释义" value="${esc(state.query)}"></div>`; }
  function status(w) { if (state.view === 'draft') return w.suspect ? tag('核对拼写', 'p-tag-wait') : `<span class="p-small">第 ${w.unit} 单元</span>`; return !w.participates ? tag('已移出', 'p-tag-neutral') : eligible(w) ? tag(w.manual ? '单独加入' : '可默写') : tag('尚未学到', 'p-tag-wait'); }
  function rowInputs(w) { return `<input class="p-cell p-cell-word ${w.suspect ? 'p-cell-suspect' : ''}" aria-label="词面 ${w.id}" data-id="${w.id}" data-field="word" value="${esc(w.word)}">${w.suspect ? `<button class="p-spelling" style="border:0;background:none;cursor:pointer" data-action="fix" data-id="${w.id}">改为 garden</button>` : ''}`; }
  function words(chat = false) {
    const list = shown(), editing = state.view === 'draft' || state.edit;
    if (!list.length) return `<div class="p-empty"><h3>${state.view === 'draft' ? '本次材料已处理' : '还没有符合条件的词条'}</h3><p>${state.view === 'draft' ? '切换演示场景，可以体验另一种导入情况。' : '可以导入照片，或修改上方的搜索条件。'}</p></div>`;
    if (chat) return `<div class="p-chat-items">${list.map(w => `<div class="p-chat-item"><label class="p-check"><input type="checkbox" aria-label="选择 ${esc(w.word)}" data-check="${w.id}" ${state.selected.includes(w.id) ? 'checked' : ''}></label><div>${editing ? rowInputs(w) + `<input class="p-cell" aria-label="释义 ${w.id}" data-id="${w.id}" data-field="meaning" value="${esc(w.meaning)}">` : `<p class="p-word-plain">${esc(w.word)}</p><p class="p-word-meaning">${esc(w.meaning)}</p>`}<div>${status(w)}</div></div></div>`).join('')}</div>`;
    return `<div class="p-list-scroll"><table class="p-word-table"><thead><tr><th><label class="p-check"><input type="checkbox" aria-label="全选当前列表" data-check="all" ${list.every(w => state.selected.includes(w.id)) ? 'checked' : ''}></label></th><th>词面</th><th>释义</th><th>${state.view === 'draft' ? '课次 / 核对' : '练习状态'}</th></tr></thead><tbody>${list.map(w => `<tr class="${state.selected.includes(w.id) ? 'p-checked' : ''}"><td><label class="p-check"><input type="checkbox" aria-label="选择 ${esc(w.word)}" data-check="${w.id}" ${state.selected.includes(w.id) ? 'checked' : ''}></label></td><td>${editing ? rowInputs(w) : `<div class="p-word-plain">${esc(w.word)}</div>`}</td><td>${editing ? `<input class="p-cell" aria-label="释义 ${w.id}" data-id="${w.id}" data-field="meaning" value="${esc(w.meaning)}">` : `<div class="p-word-meaning">${esc(w.meaning)}</div>`}</td><td>${status(w)}</td></tr>`).join('')}</tbody></table></div>`;
  }
  function saveNote() { if (state.target <= state.progress) return `保存后沿用教学进度，这 ${selected().length} 个词可以安排默写。`; if (state.choice === 'advance') return `保存后记住学到第 ${state.proposed || state.target} 单元。`; if (state.choice === 'include') return '仅这批词持续参与默写，教学进度不变。'; if (state.choice === 'keep') return '仅保存词条，不改变进度；超出进度的内容暂不自动安排。'; return '请先确认这批词的学习安排，再保存。'; }
  function footer() { return `<div class="p-bottom p-row p-between"><p class="p-small">${state.view === 'draft' ? saveNote() : '词条内容与教材信息分开展示；编辑词面或释义保留学习历史。'}</p>${state.view === 'draft' ? button(`保存 ${selected().length} 个词`, 'save', (!selected().length || (state.target > state.progress && !state.choice)) ? 'disabled' : '', 'p-btn-primary') : button('自动安排默写', 'start-auto', '', 'p-btn-primary')}</div>`; }
  function receipt() { if (!state.receipt) return ''; const r = state.receipt; return `<section class="p-receipt"><div class="p-row p-between"><div><h3>已保存 ${r.count} 个词，${r.eligible} 个可以安排默写</h3><p>${esc(r.note)}</p></div>${r.eligible ? button('默写这次导入的词', 'start-import', '', 'p-btn-primary') : button('把这批词加入默写', 'include-import')}</div></section>`; }
  function variantA() { return `<div class="p-top p-row p-between"><div><h2>${state.view === 'draft' ? '核对这次导入' : '整理词库'}</h2><p class="p-muted">看着原图改词，学习安排一起确认。</p></div>${tag('英语', 'p-tag-neutral')}</div>${stateLine()}${receipt()}<div class="p-workspace"><aside class="p-stack"><section class="p-surface p-pad">${sourcePanel()}</section><section class="p-surface p-pad">${coach()}</section></aside><section class="p-surface">${tabs()}<div class="p-bar"><h3>${state.view === 'draft' ? `小学英语 · 第 ${state.target} 单元` : '小学英语 · 四年级上册'}</h3><p class="p-small">${state.view === 'draft' ? '点击词面或释义直接修改，勾选后可批量处理。' : '39 个示例词条；课次为演示归属。'}</p></div>${toolbar()}${words()}${footer()}</section></div>`; }
  function variantB() { return `<div class="p-chat-shell"><div class="p-top"><h2>把学习材料交给小天</h2><p class="p-muted">先核对内容，再用一句话告诉小天怎么安排。</p></div>${stateLine()}${receipt()}${state.view === 'draft' && state.draft.length ? `<div class="p-chat-user"><div class="p-file"><div class="p-file-icon">词表</div><div><strong>${esc(state.fileName)}</strong><p>第 ${state.target} 单元的学习材料</p></div></div></div>` : ''}<div class="p-chat-message"><div class="p-chat-name">小天</div><div class="p-chat-body"><p>${state.view === 'draft' ? state.draft.length ? `识别到 ${state.draft.length} 个词，属于小学英语第 ${state.target} 单元。先看看有没有需要改的地方。` : '这次材料已保存，可以在词库里继续整理。' : '这是已经保存的词条。选中多条就能一起编辑。'}</p><section class="p-chat-card">${tabs()}${toolbar()}${words(true)}</section>${state.view === 'draft' ? `<p>${state.target > state.progress ? question() : `这批内容在已学范围内，沿用第 ${state.progress} 单元的进度。`}</p><div style="margin-top:14px">${choices(true)}</div>` : ''}${footer()}</div></div><section class="p-chat-composer">${command()}<div class="p-row" style="margin-top:8px">${button('选择照片', 'upload', '', 'p-btn-quiet p-btn-small')}<span class="p-small">可说“学到第四单元了”或“这批先加入默写”</span></div>${state.messages.length ? `<div class="p-notice">${esc(state.messages.at(-1))}</div>` : ''}<input id="p-upload" type="file" accept="image/*" hidden></section></div>`; }
  function variantC() { return `<div class="p-top"><h2>按教材整理学习内容</h2><p class="p-muted">教学进度留在教材上，词条在单元里连续编辑。</p></div>${receipt()}<div class="p-notebook"><aside class="p-book-nav"><div class="p-book-cover"><span>已登记教材 · 英语</span><h3>小学英语</h3><p>四年级 · 上册</p></div><p class="p-small" style="margin-bottom:12px">已学到 <b>${progress(state.progress)}</b></p><div class="p-book-units"><button class="p-unit ${!state.filter ? 'active' : ''}" data-action="filter" data-unit="0">全部内容 <small>${state.words.length} 词</small></button>${[1,2,3,4].map(n => `<button class="p-unit ${state.filter === n ? 'active' : ''}" data-action="filter" data-unit="${n}">第 ${n} 单元 <small>${n <= state.progress ? '已学到' : '尚未学到'}</small></button>`).join('')}</div><div style="margin-top:20px">${button('选择词表照片', 'upload', '', 'p-btn-small')}<input id="p-upload" type="file" accept="image/*" hidden><p class="p-small" style="margin-top:12px">可安排默写 ${state.words.filter(eligible).length} 词</p></div></aside><section class="p-book-body"><div class="p-book-head"><div class="p-steps"><span class="p-step active">${state.view === 'draft' ? '核对内容' : '整理词库'}</span><span class="p-step ${state.choice ? 'active' : ''}">确认学习安排</span><span class="p-step ${state.receipt ? 'active' : ''}">保存并练习</span></div><h2>${state.filter ? `第 ${state.filter} 单元` : state.view === 'draft' ? `导入第 ${state.target} 单元` : '全部词条'}</h2><p class="p-muted">${state.view === 'draft' ? `${esc(state.fileName)} · 示例识别 ${state.draft.length} 词` : '教材归属集中在左侧，词面和释义留在主区域。'}</p></div>${tabs()}${toolbar()}${words()}<div class="p-pad">${coach(true)}</div>${footer()}</section></div>`; }
  function inspector() { return `<details class="p-state"><summary>查看演示状态：进度 ${progress(state.progress)}，可练 ${state.words.filter(eligible).length} 词，待保存 ${state.draft.length} 词</summary><dl><dt>教学进度</dt><dd>${progress(state.progress)}</dd><dt>本次选择</dt><dd>${({ advance: `更新到第 ${state.proposed || state.target} 单元`, include: '仅加入本批词条', keep: '只保存' })[state.choice] || '尚未选择 / 沿用进度'}</dd><dt>复习安排更新</dt><dd>${state.reviewUpdates} 个词（模拟）</dd><dt>操作记录</dt><dd>${state.events.map(esc).join('<br>')}</dd></dl><details><summary>完整相关状态</summary><pre>${esc(JSON.stringify({ ...state, backup: undefined }, null, 2))}</pre></details></details>`; }
  function render() {
    root.innerHTML = reviewBar() + ({ A: variantA, B: variantB, C: variantC })[variant]() + inspector();
    $('.p-switcher')?.remove();
    const switcher = document.createElement('nav'); switcher.className = 'p-switcher'; switcher.setAttribute('aria-label', '切换原型方案');
    switcher.innerHTML = `<button type="button" aria-label="上一个方案" data-cycle="-1">←</button><strong>${variant} · ${names[variant]}</strong><button type="button" aria-label="下一个方案" data-cycle="1">→</button>`;
    switcher.onclick = e => { const b = e.target.closest('[data-cycle]'); if (b) cycle(Number(b.dataset.cycle)); }; document.body.append(switcher);
    // The inspector always reflects edits, without re-rendering focused inputs.
  }
  function cycle(delta) { variant = ['A','B','C'][(['A','B','C'].indexOf(variant) + delta + 3) % 3]; const u = new URL(location.href); u.searchParams.set('variant', variant); history.replaceState(null, '', u); render(); }
  function refreshInspector() { const old = $('.p-state', root); const wrapper = document.createElement('div'); wrapper.innerHTML = inspector(); if (old) { const open = old.open; old.replaceWith(wrapper.firstElementChild); $('.p-state', root).open = open; } }
  function saveDraft() {
    const list = selected(); if (!list.length) return showToast('请先选择要保存的词条');
    if (list.some(w => !w.word.trim())) return showToast('词面不能为空，请先补齐');
    if (state.target > state.progress && !state.choice) return showToast('请先确认学习安排');
    if (state.choice === 'advance') state.progress = state.proposed || state.target;
    const saved = [];
    for (const w of list) {
      const existing = state.words.find(x => x.word === w.word.trim());
      if (existing) { if (state.choice === 'include') existing.manual = true; saved.push(existing.id); }
      else { const item = { ...w, word: w.word.trim(), manual: state.choice === 'include', suspect: false }; state.words.push(item); saved.push(item.id); }
    }
    const note = state.choice === 'include' ? '这批词持续参与默写，教学进度保持不变。' : state.choice === 'advance' ? `教学进度已记为第 ${state.progress} 单元，之前的单元也可以安排。` : state.choice === 'keep' ? '词条已保存，教学进度未改变。' : '已沿用原教学进度。';
    state.receipt = { count: saved.length, ids: saved, eligible: state.words.filter(w => saved.includes(w.id) && eligible(w)).length, note };
    state.draft = state.draft.filter(w => !state.selected.includes(w.id)); state.view = 'library'; state.selected = []; state.choice = null; state.filter = 0; state.query = ''; log(`保存 ${saved.length} 词；${note}`); render();
  }
  function beginEdit() { if (!state.edit) { state.backup = structuredClone(state.words); state.edit = true; } }
  function instruction(text) {
    const digits = { 一:1, 二:2, 三:3, 四:4, 五:5, 六:6, 七:7, 八:8, 九:9, 十:10 };
    const hit = text.match(/(?:第\s*)?([1-9一二三四五六七八九十])\s*(?:单元|课)/); const n = hit ? Number(hit[1]) || digits[hit[1]] : null;
    if (/(默写|听写|练习|练一下)/.test(text) && !/(加入|先保存|学到)/.test(text)) { startLesson(n); return; }
    if (n && /(学到|学完|进度)/.test(text)) {
      if (state.view === 'draft') { state.choice = 'advance'; state.proposed = n; state.messages.push(`明白，保存这批词时，会把教学进度记为第 ${n} 单元。`); }
      else { state.progress = n; state.messages.push(`已记住学到第 ${n} 单元，第 1～${n} 单元可以安排默写。`); log(`明确更新进度到第 ${n} 单元`); }
    } else if (state.view === 'draft' && /(加入|这批|这些.*练)/.test(text)) { state.choice = 'include'; state.messages.push('保存时只把这批词加入默写，教学进度不变。'); }
    else if (state.view === 'draft' && /(都学过|都学完)/.test(text)) { state.choice = 'advance'; state.proposed = state.target; state.messages.push(`保存时记住已学到第 ${state.target} 单元。`); }
    else if (state.view === 'draft' && /(保存|还没学|暂不)/.test(text)) { state.choice = 'keep'; state.messages.push('先保存词条，保持教学进度，暂不加入自动默写。'); }
    else state.messages.push('这是固定回复演示。可试“现在学到第四单元了”“这批先加入默写”或“今天默写第三单元”。');
    render();
  }
  function openModal(title, html, actions) {
    modalReturnFocus = document.activeElement;
    const shade = document.createElement('div'); shade.className = 'p-modal-shade p-root';
    shade.innerHTML = `<section class="p-modal" role="dialog" aria-modal="true" aria-labelledby="p-dialog-title"><div class="p-modal-head p-row p-between"><h3 id="p-dialog-title">${esc(title)}</h3>${button('关闭','close-modal','','p-btn-quiet')}</div><div class="p-modal-content">${html}</div><div class="p-bottom p-row p-between">${actions}</div></section>`;
    document.body.append(shade); shade.querySelector('textarea,input,select,button')?.focus();
    shade.addEventListener('click', e => { if (e.target === shade || e.target.closest('[data-action="close-modal"]')) closeModal(); });
    return shade;
  }
  function closeModal() { $('.p-modal-shade')?.remove(); modalReturnFocus?.focus(); }
  function pasteModal() {
    const list = selected().length ? selected() : shown(); if (!list.length) return showToast('没有可以编辑的词条');
    const modal = openModal(`批量编辑 ${list.length} 个词`, `<p>每行“词面 + Tab + 释义”，与选中词条顺序一一对应。可以从表格复制；词条身份和学习历史保留。</p><textarea aria-label="批量词面和释义" id="p-paste">${esc(list.map(w => `${w.word}\t${w.meaning}`).join('\n'))}</textarea><p class="p-error" id="p-paste-error"></p><div id="p-diffs"></div>`, `<span class="p-small">先预览，再应用修改</span>${button('预览修改','preview-paste','','p-btn-primary')}`);
    let changes;
    modal.addEventListener('click', e => {
      const action = e.target.closest('[data-action]')?.dataset.action;
      if (action === 'preview-paste') {
        const lines = $('#p-paste').value.trim().split(/\r?\n/).filter(x => x.trim());
        if (lines.length !== list.length || lines.some(x => !/^\S.*\t/.test(x))) { $('#p-paste-error').textContent = `需要 ${list.length} 行，每行用 Tab 分隔词面和释义。`; return; }
        changes = lines.map((line, i) => { const [word, ...meaning] = line.split('\t'); return { id: list[i].id, word: word.trim(), meaning: meaning.join('\t').trim() }; });
        if (new Set(changes.map(c => c.word)).size !== changes.length) { $('#p-paste-error').textContent = '修改后词面重复，请先核对。'; return; }
        $('#p-paste-error').textContent = ''; const diff = changes.filter((c, i) => c.word !== list[i].word || c.meaning !== list[i].meaning);
        $('#p-diffs').innerHTML = diff.length ? diff.map(c => { const old = list.find(w => w.id === c.id); return `<div class="p-diff"><del>${esc(old.word)} / ${esc(old.meaning)}</del><ins>${esc(c.word)} / ${esc(c.meaning)}</ins></div>`; }).join('') : '<p>没有修改内容。</p>';
        const b = $('[data-action="preview-paste"]', modal); b.dataset.action = 'apply-paste'; b.textContent = `应用 ${diff.length} 处修改`;
      } else if (action === 'apply-paste') {
        if (state.view === 'library') beginEdit(); changes.forEach(c => { const w = (state.view === 'draft' ? state.draft : state.words).find(x => x.id === c.id); Object.assign(w, c, { suspect: false }); });
        closeModal(); render(); showToast(state.view === 'library' ? '修改已应用，点击“保存修改”完成' : '已更新待保存词条');
      }
    });
    $('#p-paste', modal).addEventListener('input', () => { const b = $('[data-action="apply-paste"]', modal); if (b) { b.dataset.action = 'preview-paste'; b.textContent = '重新预览修改'; } changes = null; });
  }
  function assignModal() {
    const list = selected(); if (!list.length) return showToast('请先勾选需要指定课次的词条');
    const modal = openModal(`为 ${list.length} 个词指定课次`, `<p>教材：小学英语 · 四年级上册</p><label for="p-assign">课次</label> <select id="p-assign">${[1,2,3,4].map(n => `<option value="${n}">第 ${n} 单元</option>`).join('')}</select><p class="p-muted">只修改所选词条的归属，不自动更新教材进度。</p>`, `<span></span>${button('应用课次','apply-unit','','p-btn-primary')}`);
    modal.addEventListener('click', e => { if (e.target.closest('[data-action="apply-unit"]')) { if (state.view === 'library') beginEdit(); const n = Number($('#p-assign').value); list.forEach(w => { w.unit = n; }); log(`为 ${list.length} 词指定第 ${n} 单元`); closeModal(); render(); } });
  }
  function startLesson(unit = null, mode = 'specified') {
    if (state.edit) return showToast('请先保存或取消正在编辑的词条');
    let list = state.words.filter(eligible);
    if (mode === 'import') list = list.filter(w => state.receipt?.ids.includes(w.id));
    else if (unit) list = list.filter(w => w.unit === unit);
    if (mode === 'auto') list = list.filter(w => w.due);
    if (!list.length) return showToast(mode === 'auto' && state.words.some(eligible) ? '现在没有到期词，可以说“默写第三单元”来指定练习' : '这些词尚未加入默写。可以更新教学进度，或勾选后单独加入');
    const sessionWords = structuredClone(list);
    const title = mode === 'auto' ? '自动安排默写' : unit ? `指定默写：第 ${unit} 单元` : '指定默写：本次导入';
    const modal = openModal(`${title} · ${list.length} 个词`, `<p class="p-notice">课堂结果演示：不播放语音。${mode !== 'auto' ? `已包含 ${list.filter(w => !w.due).length} 个未到复习时间的词。` : ''}核对后，结果会更新演示中的复习安排。</p>${button('演示：全部判对','all-correct','','p-btn-small')}${list.map(w => `<div class="p-grade-row"><div><strong>${esc(w.word)}</strong><div class="p-small">${esc(w.meaning)}</div></div><select data-grade="${w.id}" aria-label="${esc(w.word)} 的结果"><option value="">尚未核对</option><option value="correct">正确</option><option value="again">需要复习</option></select></div>`).join('')}`, `<span class="p-small">完成核对后才能记录</span>${button('记录结果并更新复习','finish-lesson','disabled','p-btn-primary')}`);
    const ready = () => { $('[data-action="finish-lesson"]', modal).disabled = [...modal.querySelectorAll('[data-grade]')].some(s => !s.value); };
    modal.addEventListener('change', ready);
    modal.addEventListener('click', e => {
      const action = e.target.closest('[data-action]')?.dataset.action;
      if (action === 'all-correct') { modal.querySelectorAll('[data-grade]').forEach(s => { s.value = 'correct'; }); ready(); }
      if (action === 'finish-lesson') { modal.querySelectorAll('[data-grade]').forEach(s => { state.words.find(w => w.id === Number(s.dataset.grade)).due = s.value === 'again'; }); state.reviewUpdates += sessionWords.length; log(`记录 ${sessionWords.length} 词的默写结果，并更新复习安排（模拟）`); closeModal(); render(); showToast(`已记录 ${sessionWords.length} 个词的结果，复习安排已更新（模拟）`); }
    });
  }
  root.addEventListener('click', e => {
    const b = e.target.closest('[data-action]'); if (!b) return; const action = b.dataset.action;
    if (action === 'reset') return reset(state.scene);
    if (action === 'choice') { state.choice = b.dataset.choice; state.proposed = state.choice === 'advance' ? state.target : null; render(); }
    if (action === 'view') { if (state.edit) return showToast('请先保存或取消修改'); state.view = b.dataset.view; state.selected = state.view === 'draft' ? state.draft.map(w => w.id) : []; state.query = ''; state.filter = 0; render(); }
    if (action === 'filter') { state.filter = Number(b.dataset.unit); render(); }
    if (action === 'fix') { const w = state.draft.find(x => x.id === Number(b.dataset.id)); w.word = 'garden'; w.suspect = false; render(); }
    if (action === 'save') saveDraft();
    if (action === 'paste') pasteModal();
    if (action === 'assign') assignModal();
    if (action === 'edit') { beginEdit(); render(); }
    if (action === 'save-edits') {
      if (state.words.some(w => !w.word.trim()) || new Set(state.words.map(w => w.word.trim())).size !== state.words.length) return showToast('词面不能为空，也不能与已有词条重复');
      state.words.forEach(w => { w.word = w.word.trim(); }); state.edit = false; state.backup = null; log('已保存词条修改，保留词条身份与学习历史'); render(); showToast('修改已保存（演示）');
    }
    if (action === 'cancel-edits') { state.words = state.backup; state.edit = false; state.backup = null; render(); }
    if (action === 'include-selected' || action === 'exclude-selected') {
      if (state.edit) return showToast('请先保存或取消词条修改'); const list = selected(); if (!list.length) return showToast('请先选择词条');
      list.forEach(w => { w.participates = action === 'include-selected'; if (w.participates) w.manual = true; }); log(`${list.length} 词${action === 'include-selected' ? '持续加入' : '移出'}默写，教学进度不变`); render(); showToast('已更新默写范围，教学进度不变');
    }
    if (action === 'include-import') { state.words.filter(w => state.receipt.ids.includes(w.id)).forEach(w => { w.manual = true; w.participates = true; }); state.receipt.eligible = state.receipt.count; state.receipt.note = '已单独加入这批词，教学进度保持不变。'; log(state.receipt.note); render(); }
    if (action === 'start-import') startLesson(null, 'import');
    if (action === 'start-auto') startLesson(null, 'auto');
    if (action === 'upload') $('#p-upload').click();
  });
  root.addEventListener('submit', e => { if (!e.target.matches('.p-command')) return; e.preventDefault(); const text = new FormData(e.target).get('instruction').trim(); if (text) instruction(text); });
  root.addEventListener('input', e => {
    const el = e.target;
    if (el.dataset.field) { const w = (state.view === 'draft' ? state.draft : state.words).find(x => x.id === Number(el.dataset.id)); w[el.dataset.field] = el.value; if (el.dataset.field === 'word') w.suspect = false; refreshInspector(); }
    if (el.matches('.p-search')) { const cursor = el.selectionStart; state.query = el.value; render(); const search = $('.p-search', root); search.focus(); if (search.type !== 'search') search.setSelectionRange(cursor,cursor); }
  });
  root.addEventListener('change', e => {
    const el = e.target;
    if (el.id === 'p-scene') return reset(el.value);
    if (el.dataset.check) { const ids = el.dataset.check === 'all' ? shown().map(w => w.id) : [Number(el.dataset.check)]; state.selected = el.checked ? [...new Set([...state.selected, ...ids])] : state.selected.filter(id => !ids.includes(id)); render(); }
    if (el.id === 'p-upload' && el.files[0]) {
      if (photoURL) URL.revokeObjectURL(photoURL); photoURL = URL.createObjectURL(el.files[0]); state.fileName = el.files[0].name;
      if (!state.draft.length) { state.target = state.progress ? state.progress + 1 : 1; state.draft = fresh.map(([word,meaning], i) => ({ id: 1000 + i, word, meaning, unit: state.target, participates: true, manual: false, due: true })); }
      state.view = 'draft'; state.choice = null; state.selected = state.draft.map(w => w.id); state.filter = 0; state.query = ''; render(); showToast('照片已在本机打开，以下词条为示例识别结果');
    }
  });
  document.addEventListener('keydown', e => {
    const modal = $('.p-modal-shade');
    if (e.key === 'Escape' && modal) return closeModal();
    if (e.key === 'Tab' && modal) { const focusable = [...modal.querySelectorAll('button:not(:disabled),input,select,textarea,[tabindex="0"]')]; const first = focusable[0], last = focusable.at(-1); if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); } else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); } }
    if (modal || e.target.closest('input,textarea,select,[contenteditable]')) return;
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') { e.preventDefault(); cycle(e.key === 'ArrowLeft' ? -1 : 1); }
  });
  document.querySelectorAll('.parent-tab').forEach(tab => tab.addEventListener('click', () => activateTab(tab)));
  function activateTab(tab) {
    document.querySelectorAll('.parent-tab').forEach(t => { const active = t === tab; t.classList.toggle('active',active); t.setAttribute('aria-selected',String(active)); document.getElementById(t.getAttribute('aria-controls')).hidden = !active; });
    if (tab.id === 'parent-tab-lessons') $('#lessons-container').innerHTML = `<div class="p-root"><p>本次原型聚焦导入与词库。这是演示中的操作记录：</p><div class="p-notice">${state.events.map(esc).join('<br>')}</div></div>`;
    if (tab.id === 'parent-tab-settings') { $('#settingsSection').hidden = false; $('#settings-container').innerHTML = '<div class="p-root"><p>设置页沿用现有产品，本次不调整。请返回词库体验三个方案。</p></div>'; }
  }
  reset(); activateTab($('#parent-tab-vocab'));
  // Convenient inspection, only on this explicitly gated prototype server.
  window.__interactionPrototype = { getState: () => structuredClone(state), reset, switchVariant: key => { if (names[key]) { variant = key; const u = new URL(location.href); u.searchParams.set('variant', key); history.replaceState(null,'',u); render(); } } };
})();
