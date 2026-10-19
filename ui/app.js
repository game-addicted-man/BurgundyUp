'use strict';
(() => {
  const TOKEN = window.BURGUNDYUP.token;
  const VERSION = window.BURGUNDYUP.version;
  const $ = (id) => document.getElementById(id);
  const IMG_RE = /\.(png|jpe?g|jfif|webp|bmp|tiff?|gif)$/i;
  const FINISHED = ['done', 'error', 'canceled'];

  // ───────── 서버와 통신 ─────────
  async function api(path, body) {
    const opts = { headers: { 'X-Token': TOKEN } };
    if (body !== undefined) {
      opts.method = 'POST';
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(path, opts);
    let data = null;
    try { data = await res.json(); } catch (_) { /* 본문 없음 */ }
    if (!res.ok) {
      const err = new Error((data && data.error) || `요청이 실패했어요 (${res.status})`);
      err.status = res.status;
      throw err;
    }
    return data;
  }
  const imgURL = (id, which, v) => `/api/image?id=${encodeURIComponent(id)}&which=${which}&token=${TOKEN}${v ? `&v=${v}` : ''}`;
  const thumbURL = (id) => `/api/thumb?id=${encodeURIComponent(id)}&token=${TOKEN}`;

  // ───────── 상태 ─────────
  const S = {
    settings: null,
    models: [],
    engine: { state: 'checking', gpus: [] },
    download: { state: 'idle' },
    queue: { rev: -1, active: false, items: [] },
    defaults: {},
    platform: {},
    selected: null,
    follow: false,
    runIds: new Set(),
    runStart: 0,
    wasActive: false,
    uploading: 0,
    fails: 0,
  };
  const byId = (id) => S.queue.items.find((i) => i.id === id);

  // ───────── 도우미 ─────────
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const num = (n) => Number(n).toLocaleString('ko-KR');
  const fmtDims = (w, h) => (w && h ? `${num(w)} × ${num(h)}` : '');
  const fmtBytes = (n) => {
    if (!n) return '';
    if (n >= 1e9) return `${(n / 1e9).toFixed(1)}GB`;
    if (n >= 1e6) return `${(n / 1e6).toFixed(1)}MB`;
    if (n >= 1e3) return `${Math.round(n / 1e3)}KB`;
    return `${n}B`;
  };
  const fmtDur = (sec) => {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return `${sec}초`;
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    if (m < 60) return s ? `${m}분 ${s}초` : `${m}분`;
    return `${Math.floor(m / 60)}시간 ${m % 60}분`;
  };
  const reduceMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function toast(msg, kind = '', ms) {
    const el = document.createElement('div');
    el.className = `toast${kind ? ` is-${kind}` : ''}`;
    el.textContent = msg;
    $('toasts').appendChild(el);
    if (ms !== 0) setTimeout(() => el.remove(), ms || (kind === 'error' ? 7000 : 3800));
    return el;
  }

  // ───────── 설정 ─────────
  let settingsTimer = null;
  function patchSettings(patch, debounce) {
    Object.assign(S.settings, patch);
    renderSettings();
    const send = async () => {
      try {
        const r = await api('/api/settings', patch);
        S.settings = r.settings;
        renderSettings();
      } catch (e) {
        toast(e.message, 'error');
      }
    };
    if (debounce) {
      clearTimeout(settingsTimer);
      settingsTimer = setTimeout(send, 250);
    } else {
      send();
    }
  }

  const FORMAT_HINT = {
    png: '화질 손실이 없어요. 대신 파일이 커요.',
    jpg: '파일이 작아서 공유하기 좋아요. 투명한 배경은 지원하지 않아요.',
    webp: '작은 파일로 좋은 화질을 내요. 품질 100은 무손실이에요.',
  };

  function renderModels() {
    const list = $('modelList');
    const installed = S.models.filter((m) => m.installed);
    if (installed.length && !installed.some((m) => m.id === S.settings.model)) {
      // 고른 모델이 사라졌으면 첫 번째 모델로 바꿉니다.
      patchSettings({ model: installed[0].id });
      return;
    }
    const html = installed.map((m) => {
      const on = m.id === S.settings.model;
      return `<div class="model" role="radio" tabindex="${on ? 0 : -1}" aria-checked="${on}" data-id="${esc(m.id)}" title="${esc(m.sub)}">
        <div class="model-head"><span class="model-name">${esc(m.name)}</span>${m.tag ? `<span class="model-tag">${esc(m.tag)}</span>` : ''}${m.custom ? '<span class="model-tag is-custom">직접 추가</span>' : ''}</div>
        <div class="model-desc">${esc(m.desc)}</div>
      </div>`;
    }).join('');
    if (list.dataset.html !== html) {
      list.innerHTML = html || '<p class="hint" style="padding:10px 12px;margin:0">사용할 수 있는 모델이 없어요.</p>';
      list.dataset.html = html;
    }
    const more = S.models.filter((m) => m.downloadable && !m.installed).length;
    $('moreModels').textContent = more ? `모델 더 받기 (${more}개)` : '모델 관리';
  }

  function renderSettings() {
    const s = S.settings;
    if (!s) return;
    renderModels();
    for (const b of $('scaleSeg').querySelectorAll('button')) {
      b.setAttribute('aria-checked', String(Number(b.dataset.scale) === s.scale));
    }
    for (const b of $('formatSeg').querySelectorAll('button')) {
      b.setAttribute('aria-checked', String(b.dataset.format === s.format));
    }
    $('formatHint').textContent = FORMAT_HINT[s.format] || '';
    $('webpRow').hidden = s.format !== 'webp';
    $('webpQuality').value = s.webpQuality;
    $('webpOut').textContent = s.webpQuality === 100 ? '100 (무손실)' : String(s.webpQuality);
    for (const r of document.querySelectorAll('input[name="outmode"]')) r.checked = r.value === s.outputMode;
    $('outCaption').textContent = s.outputMode === 'same'
      ? '끌어다 놓거나 붙여넣은 이미지는 여기에 저장돼요'
      : '모든 결과를 여기에 저장해요';
    $('outPath').textContent = s.outputDir;
    $('outPath').title = s.outputDir;
    // 고급 설정
    $('tileSelect').value = String(s.tile);
    $('ttaCheck').checked = s.tta;
    for (const r of document.querySelectorAll('input[name="uimode"]')) r.checked = r.value === s.uiMode;
    renderGpuSelect();
    renderSizePreview();
    renderStart();
  }

  function renderGpuSelect() {
    const sel = $('gpuSelect');
    const gpus = S.engine.gpus || [];
    const opts = [['', gpus.length > 1 ? '자동 (외장 그래픽카드 우선)' : '자동']]
      .concat(gpus.map((g) => [String(g.id), `${g.id}번: ${g.name}`]));
    if (S.settings.gpu && !gpus.some((g) => String(g.id) === S.settings.gpu)) {
      opts.push([S.settings.gpu, `${S.settings.gpu}번 GPU`]);
    }
    const html = opts.map(([v, t]) => `<option value="${esc(v)}">${esc(t)}</option>`).join('');
    if (sel.dataset.html !== html) {
      sel.innerHTML = html;
      sel.dataset.html = html;
    }
    sel.value = S.settings.gpu || '';
  }

  function renderSizePreview() {
    const el = $('sizePreview');
    const it = byId(S.selected) || S.queue.items.find((i) => i.status === 'ready');
    if (!it || !it.w) {
      el.textContent = S.queue.items.length ? '' : '이미지를 넣으면 결과 크기를 알려 드려요.';
      return;
    }
    const k = S.settings.scale;
    el.innerHTML = `<strong>${fmtDims(it.w, it.h)}</strong><span class="arrow">→</span><strong>${fmtDims(it.w * k, it.h * k)}</strong>`;
  }

  // ───────── 시작 버튼과 진행 상황 ─────────
  function renderStart() {
    const btn = $('startBtn');
    const items = S.queue.items;
    const ready = items.filter((i) => i.status === 'ready').length;
    const finished = items.filter((i) => FINISHED.includes(i.status)).length;
    const engineOK = S.engine.state !== 'missing';
    btn.classList.toggle('is-stop', S.queue.active);
    if (S.queue.active) {
      btn.textContent = '멈추기';
      btn.disabled = false;
    } else if (ready) {
      btn.textContent = ready > 1 ? `업스케일 시작 (${ready}장)` : '업스케일 시작';
      btn.disabled = !engineOK;
    } else if (finished) {
      btn.textContent = finished > 1 ? `모두 다시 업스케일 (${finished}장)` : '다시 업스케일';
      btn.disabled = !engineOK;
    } else {
      btn.textContent = '업스케일 시작';
      btn.disabled = true;
    }
    renderRun();
  }

  function renderRun() {
    const box = $('runInfo');
    if (!S.queue.active) {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    let sum = 0;
    let done = 0;
    let cur = null;
    let total = 0;
    for (const id of S.runIds) {
      const it = byId(id);
      if (!it) continue;
      total++;
      if (FINISHED.includes(it.status)) {
        sum += 100;
        done++;
      } else if (it.status === 'running') {
        sum += it.progress;
        cur = it;
      }
    }
    total = Math.max(total, 1);
    const frac = Math.min(1, sum / (100 * total));
    $('runCount').textContent = `처리 중 ${Math.min(done + 1, total)} / ${total}장`;
    $('runPct').textContent = `${Math.floor(frac * 100)}%`;
    $('runBar').style.width = `${(frac * 100).toFixed(1)}%`;
    $('runFile').textContent = cur ? cur.name : '';
    const elapsed = (Date.now() - S.runStart) / 1000;
    $('runEta').textContent = frac > 0.04 && elapsed > 4 ? `남은 시간 약 ${fmtDur((elapsed * (1 - frac)) / frac)}` : '';
  }

  // ───────── 엔진 상태 ─────────
  function renderEngine() {
    const e = S.engine;
    const st = $('engineStatus');
    const bad = e.state === 'error' || e.state === 'missing';
    st.classList.toggle('is-ok', e.state === 'ok');
    st.classList.toggle('is-error', bad);
    const gpus = e.gpus || [];
    let text = '엔진 확인 중';
    if (e.state === 'ok') {
      const chosen = S.settings && S.settings.gpu !== '' ? gpus.find((g) => String(g.id) === S.settings.gpu) : null;
      if (chosen) text = `준비 완료 (${chosen.name})`;
      else if (gpus.length === 1) text = `준비 완료 (${gpus[0].name})`;
      else if (gpus.length > 1) text = `준비 완료 (GPU ${gpus.length}개)`;
      else text = '준비 완료';
    } else if (bad) {
      text = e.state === 'missing' ? '엔진 파일이 없어요' : '엔진에 문제가 있어요';
    }
    $('engineText').textContent = text;
    st.title = e.message || '';

    $('banner').hidden = !bad;
    $('bannerText').textContent = e.message || '';

    $('engineMsg').textContent = e.state === 'ok' ? '엔진과 그래픽카드가 정상이에요.' : (e.message || '확인 중이에요.');
    $('gpuList').innerHTML = gpus.map((g) => `<li>${g.id}번: ${esc(g.name)}</li>`).join('');
    $('engineDetailWrap').hidden = !e.detail;
    $('engineDetail').textContent = e.detail || '';
    if (S.settings) renderGpuSelect();
    renderStart();
  }

  // ───────── 필름 띠(작업 목록) ─────────
  const tiles = new Map();
  const CHECK_SVG = '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="M2.5 6.3l2.3 2.3 4.7-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';

  function makeTile(it) {
    const el = document.createElement('button');
    el.type = 'button';
    el.className = 'tile';
    el.dataset.id = it.id;
    const ext = (it.name.split('.').pop() || '').toUpperCase();
    el.innerHTML = `<img alt="" loading="lazy" decoding="async"><span class="tile-ext" hidden>${esc(ext)}</span>
      <span class="tile-bar" hidden><span></span></span><span class="tile-badge" hidden></span>
      <span class="tile-x" role="button" aria-label="목록에서 빼기" title="목록에서 빼기">×</span>`;
    const img = el.querySelector('img');
    img.onerror = () => { img.hidden = true; el.querySelector('.tile-ext').hidden = false; };
    img.src = thumbURL(it.id);
    el.addEventListener('click', (ev) => {
      if (ev.target.closest('.tile-x')) {
        ev.stopPropagation();
        removeItem(it.id);
        return;
      }
      S.follow = false;
      select(it.id);
    });
    return el;
  }

  function updateTile(el, it) {
    const st = `st-${it.status}`;
    if (el.dataset.st !== st) {
      el.classList.remove(el.dataset.st || 'st-none');
      el.classList.add(st);
      el.dataset.st = st;
    }
    el.classList.toggle('is-selected', it.id === S.selected);
    el.title = [it.name, fmtDims(it.w, it.h)].filter(Boolean).join('\n');
    const bar = el.querySelector('.tile-bar');
    bar.hidden = it.status !== 'running';
    bar.firstElementChild.style.width = `${it.progress}%`;
    const badge = el.querySelector('.tile-badge');
    let cls = '';
    let html = '';
    if (it.status === 'done') { cls = 'is-done'; html = CHECK_SVG; }
    else if (it.status === 'error') { cls = 'is-error'; html = '!'; }
    else if (it.status === 'queued') { cls = 'is-wait'; html = '대기'; }
    else if (it.status === 'canceled') { cls = 'is-wait'; html = '멈춤'; }
    badge.hidden = !cls;
    if (badge.dataset.k !== cls) {
      badge.className = `tile-badge ${cls}`;
      badge.innerHTML = html;
      badge.dataset.k = cls;
    }
  }

  function renderStrip() {
    const items = S.queue.items;
    const has = items.length > 0;
    $('strip').hidden = !has;
    $('empty').hidden = has;
    $('viewer').hidden = !has;
    const scroll = $('stripScroll');
    const seen = new Set(items.map((i) => i.id));
    for (const [id, el] of tiles) {
      if (!seen.has(id)) {
        el.remove();
        tiles.delete(id);
      }
    }
    let anchor = scroll.firstElementChild;
    for (const it of items) {
      let el = tiles.get(it.id);
      if (!el) {
        el = makeTile(it);
        tiles.set(it.id, el);
      }
      updateTile(el, it);
      if (el !== anchor) scroll.insertBefore(el, anchor);
      else anchor = anchor.nextElementSibling;
    }
    const done = items.filter((i) => i.status === 'done').length;
    const bad = items.filter((i) => i.status === 'error').length;
    const parts = [`<b>총 ${num(items.length)}장</b>`];
    const sub = [];
    if (done) sub.push(`완료 ${num(done)}장`);
    if (bad) sub.push(`실패 ${num(bad)}장`);
    if (sub.length) parts.push(sub.join(', '));
    $('stripCount').innerHTML = parts.join('<br>');
    $('clearDone').disabled = !done;
    $('clearAll').disabled = S.queue.active || !items.some((i) => i.status !== 'running' && i.status !== 'queued');
  }

  function select(id, scroll = true) {
    if (S.selected === id) return;
    S.selected = id;
    for (const [tid, el] of tiles) el.classList.toggle('is-selected', tid === id);
    if (scroll) {
      const el = tiles.get(id);
      if (el) el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    }
    renderViewer();
    renderSizePreview();
  }

  function selectRel(delta) {
    const items = S.queue.items;
    if (!items.length) return;
    const i = items.findIndex((x) => x.id === S.selected);
    const n = Math.min(items.length - 1, Math.max(0, (i < 0 ? 0 : i + delta)));
    S.follow = false;
    select(items[n].id);
  }

  // ───────── 미리보기(비교 보기) ─────────
  const V = {
    key: '', id: null, cw: 0, ch: 0, s: 1, tx: 0, ty: 0, fit: true, div: 0.5,
    hasAfter: false, beforeFailed: false, loadToken: 0, animateNext: false,
  };

  function viewportSize() {
    const vp = $('viewport');
    return [vp.clientWidth, vp.clientHeight];
  }

  function applyTransform() {
    const c = $('canvas');
    c.style.width = `${V.cw}px`;
    c.style.height = `${V.ch}px`;
    c.style.transform = `translate(${V.tx}px, ${V.ty}px) scale(${V.s})`;
    c.style.setProperty('--s', String(V.s));
    $('zoomText').textContent = V.cw ? `${Math.round(V.s * 100)}%` : '';
    updateClip();
  }

  function updateClip() {
    const [w] = viewportSize();
    const dx = V.div * w;
    $('divider').style.left = `${dx}px`;
    const lx = Math.max(0, Math.min(V.cw, (dx - V.tx) / V.s));
    $('imgAfter').style.clipPath = `inset(0 0 0 ${lx}px)`;
  }

  const fitScale = () => {
    const [w, h] = viewportSize();
    if (!V.cw || !V.ch || !w || !h) return 1;
    return Math.min((w - 32) / V.cw, (h - 32) / V.ch);
  };
  const clampS = (s) => Math.min(32, Math.max(Math.min(0.05, fitScale() * 0.5), s));

  function fitView() {
    const [w, h] = viewportSize();
    if (!V.cw || !V.ch || !w || !h) return;
    V.s = clampS(fitScale());
    V.tx = (w - V.cw * V.s) / 2;
    V.ty = (h - V.ch * V.s) / 2;
    V.fit = true;
    applyTransform();
  }

  function zoomAt(factor, mx, my) {
    if (!V.cw) return;
    const ns = clampS(V.s * factor);
    V.tx = mx - ((mx - V.tx) * ns) / V.s;
    V.ty = my - ((my - V.ty) * ns) / V.s;
    V.s = ns;
    V.fit = false;
    applyTransform();
  }
  const zoomCenter = (f) => { const [w, h] = viewportSize(); zoomAt(f, w / 2, h / 2); };
  const zoom100 = () => { const [w, h] = viewportSize(); zoomAt(1 / V.s, w / 2, h / 2); };

  function setCanvasSize(cw, ch, keepVisual) {
    if (!cw || !ch) return;
    if (cw === V.cw && ch === V.ch && keepVisual) return;
    const old = V.cw;
    V.cw = cw;
    V.ch = ch;
    if (V.fit || !keepVisual || !old) {
      fitView();
    } else {
      // 같은 이미지가 결과 크기로 바뀌면 화면에 보이는 크기를 그대로 유지합니다.
      const [w, h] = viewportSize();
      const k = old / cw;
      const cx = w / 2;
      const cy = h / 2;
      const ns = clampS(V.s * k);
      V.tx = cx - ((cx - V.tx) * (V.s * k)) / V.s;
      V.ty = cy - ((cy - V.ty) * (V.s * k)) / V.s;
      V.s = ns;
      applyTransform();
    }
  }

  function revealAfter(animate) {
    V.hasAfter = true;
    $('divider').hidden = false;
    $('tagRight').hidden = false;
    $('imgAfter').hidden = false;
    const div = $('divider');
    const after = $('imgAfter');
    if (animate && !reduceMotion()) {
      V.div = 1;
      div.classList.remove('is-entering');
      after.style.transition = 'none';
      updateClip();
      requestAnimationFrame(() => requestAnimationFrame(() => {
        div.classList.add('is-entering');
        after.style.transition = 'clip-path .5s cubic-bezier(.2,.8,.2,1)';
        V.div = 0.5;
        updateClip();
        setTimeout(() => { div.classList.remove('is-entering'); after.style.transition = ''; }, 650);
      }));
    } else {
      updateClip();
    }
  }

  function hideAfter() {
    V.hasAfter = false;
    $('divider').hidden = true;
    $('tagRight').hidden = true;
    const after = $('imgAfter');
    after.hidden = true;
    after.removeAttribute('src');
    after.dataset.src = '';
  }

  function loadImages(it, wantAfter, key) {
    const sameItem = V.id === it.id;
    V.key = key;
    V.id = it.id;
    const token = ++V.loadToken;
    const before = $('imgBefore');
    const after = $('imgAfter');
    const bURL = imgURL(it.id, 'in');
    const targetW = wantAfter && it.outW ? it.outW : it.w;
    const targetH = wantAfter && it.outH ? it.outH : it.h;
    if (!sameItem) {
      V.beforeFailed = false;
      V.fit = true;
      hideAfter();
      if (!(targetW && targetH)) { V.cw = 0; V.ch = 0; }
    }
    if (targetW && targetH) setCanvasSize(targetW, targetH, sameItem);

    if (before.dataset.src !== bURL) {
      before.dataset.src = bURL;
      before.onload = () => {
        if (token !== V.loadToken) return;
        if (!V.cw) setCanvasSize(before.naturalWidth, before.naturalHeight, false);
      };
      before.onerror = () => {
        if (token !== V.loadToken) return;
        V.beforeFailed = true;
        renderViewer();
      };
      before.src = bURL;
    }

    if (wantAfter) {
      const aURL = imgURL(it.id, 'out', it.outSize);
      const animate = V.animateNext;
      V.animateNext = false;
      if (after.dataset.src !== aURL) {
        after.dataset.src = aURL;
        after.hidden = true;
        after.onload = () => {
          if (token !== V.loadToken) return;
          if (!it.outW) setCanvasSize(after.naturalWidth, after.naturalHeight, true);
          revealAfter(animate);
        };
        after.onerror = () => { if (token === V.loadToken) hideAfter(); };
        after.src = aURL;
      }
    } else if (V.hasAfter || after.getAttribute('src')) {
      hideAfter();
    }
  }

  function setNotice(n) {
    const box = $('notice');
    if (!n) {
      box.hidden = true;
      box.dataset.key = '';
      return;
    }
    if (box.dataset.key === n.key) return;
    box.dataset.key = n.key;
    box.hidden = false;
    box.classList.toggle('is-error', n.kind === 'error');
    $('noticeTitle').textContent = n.title;
    $('noticeText').textContent = n.text || '';
    $('noticeText').hidden = !n.text;
    $('noticeDetailWrap').hidden = !n.detail;
    $('noticeDetailWrap').open = false;
    $('noticeDetail').textContent = n.detail || '';
    const acts = $('noticeActions');
    acts.innerHTML = '';
    for (const [label, fn] of n.actions || []) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn btn-small';
      b.textContent = label;
      b.addEventListener('click', fn);
      acts.appendChild(b);
    }
  }

  function renderViewer() {
    const it = byId(S.selected);
    if (!it) return;
    const done = it.status === 'done' && it.out;
    const tooBig = done && it.outW * it.outH > 64e6;
    const wantAfter = done && !tooBig;
    const key = `${it.id}|${wantAfter ? it.out + it.outSize : ''}`;
    if (key !== V.key) loadImages(it, wantAfter, key);

    $('vName').textContent = it.name;
    if (done) {
      const extra = [it.model, it.scale ? `${it.scale}배` : '', fmtBytes(it.outSize)].filter(Boolean).join(', ');
      $('vDims').innerHTML = `${esc(fmtDims(it.w, it.h))}<span class="arrow">→</span>${esc(fmtDims(it.outW, it.outH))}${extra ? ` (${esc(extra)})` : ''}`;
    } else {
      $('vDims').textContent = [fmtDims(it.w, it.h), fmtBytes(it.size)].filter(Boolean).join(', ');
    }
    $('vDims').title = done ? it.out : (it.origin === 'path' ? it.src : '');
    $('openResult').disabled = !done;
    $('revealResult').disabled = !done;

    const scan = $('scan');
    scan.hidden = it.status !== 'running';
    if (it.status === 'running') scan.style.top = `${Math.min(100, it.progress)}%`;

    const chip = $('chip');
    let chipText = '';
    if (it.status === 'running') chipText = `처리 중 ${Math.floor(it.progress)}%`;
    else if (it.status === 'queued') chipText = '차례를 기다리는 중';
    else if (it.status === 'ready') chipText = '업스케일 시작을 누르면 처리돼요';
    else if (it.status === 'canceled') chipText = '멈췄어요';
    chip.hidden = !chipText;
    chip.textContent = chipText;

    if (it.status === 'error') {
      setNotice({
        key: `err|${it.id}|${it.error}`, kind: 'error', title: '업스케일하지 못했어요', text: it.error, detail: it.detail,
        actions: [['다시 시도', () => retry(it.id)], ['목록에서 빼기', () => removeItem(it.id)]],
      });
    } else if (tooBig) {
      setNotice({
        key: `big|${it.id}`, title: '결과가 커서 미리보기를 건너뛰었어요',
        text: `${fmtDims(it.outW, it.outH)} 크기예요. '결과 열기'로 확인해 주세요.`,
        actions: [['결과 열기', () => openItem(it.id, 'file')], ['폴더에서 보기', () => openItem(it.id, 'folder')]],
      });
    } else if (V.beforeFailed && !done) {
      setNotice({ key: `nopreview|${it.id}`, title: '미리 볼 수 없는 형식이에요', text: 'TIFF 같은 형식은 미리보기 없이 업스케일만 할 수 있어요.' });
    } else {
      setNotice(null);
    }
  }

  // ───────── 상태 반영 ─────────
  function applyQueue(q) {
    if (!q) return;
    if (q.unchanged) {
      S.queue.active = q.active;
      return;
    }
    const prev = new Map(S.queue.items.map((i) => [i.id, i]));
    const prevIndex = S.queue.items.findIndex((i) => i.id === S.selected);
    S.queue = q;
    for (const it of q.items) {
      if (it.status === 'queued' || it.status === 'running') S.runIds.add(it.id);
      const p = prev.get(it.id);
      if (p && p.status === 'running' && it.status === 'done' && it.id === S.selected) V.animateNext = true;
      if (p && p.status !== 'done' && it.status === 'done' && it.note) toast(it.note);
    }
    if (q.active && !S.wasActive) {
      S.runStart = Date.now();
      S.follow = true;
    }
    if (!q.active && S.wasActive) finishRun();
    S.wasActive = q.active;

    // 선택 유지·보정
    if (S.follow && q.active) {
      const running = q.items.find((i) => i.status === 'running');
      if (running && running.id !== S.selected) select(running.id);
    }
    if (!byId(S.selected)) {
      const n = q.items.length;
      S.selected = null;
      if (n) select(q.items[Math.min(Math.max(prevIndex, 0), n - 1)].id);
    }
  }

  function finishRun() {
    let ok = 0;
    let bad = 0;
    let stopped = 0;
    for (const id of S.runIds) {
      const it = byId(id);
      if (!it) continue;
      if (it.status === 'done') ok++;
      else if (it.status === 'error') bad++;
      else stopped++;
    }
    S.runIds.clear();
    if (bad) toast(`${ok}장 완료, ${bad}장은 실패했어요. 실패한 이미지를 눌러 이유를 확인하세요.`, 'error');
    else if (ok && !stopped) toast(ok > 1 ? `${ok}장 모두 끝났어요.` : '업스케일이 끝났어요.', 'ok');
    else if (stopped) toast(ok ? `${ok}장을 처리하고 멈췄어요.` : '작업을 멈췄어요.');
  }

  let lastDl = 'idle';
  function applyDownload(d) {
    if (!d) return;
    const was = lastDl;
    S.download = d;
    lastDl = d.state;
    if (was === 'running' && d.state === 'done') toast(`'${d.name}' 모델을 설치했어요.`, 'ok');
    if (was === 'running' && d.state === 'error') toast(d.error || '모델을 내려받지 못했어요.', 'error');
    if ($('modelsDlg').open) renderDownloads();
  }

  function renderAll() {
    renderSettings();
    renderEngine();
    renderStrip();
    renderViewer();
    renderStart();
  }

  // ───────── 주기적으로 상태 받기 ─────────
  let pollTimer = null;
  async function poll() {
    clearTimeout(pollTimer);
    try {
      const r = await api(`/api/status?rev=${S.queue.rev}`);
      S.fails = 0;
      S.engine = r.engine || S.engine;
      if (r.models) S.models = r.models;
      applyDownload(r.download);
      applyQueue(r.queue);
      renderAll();
    } catch (e) {
      S.fails++;
      if (S.fails === 3) {
        $('banner').hidden = false;
        $('bannerText').textContent = '프로그램과 연결이 끊겼어요. 창을 닫고 BurgundyUp.exe 를 다시 실행해 주세요.';
      }
    }
    const fast = S.queue.active || S.download.state === 'running' || S.engine.state === 'checking' || S.uploading > 0;
    pollTimer = setTimeout(poll, fast ? 400 : 1500);
  }
  const pollNow = () => setTimeout(poll, 30);

  // ───────── 동작 ─────────
  async function pick(kind) {
    try {
      const r = await api('/api/pick', { kind });
      if (r.canceled) return;
      if (kind === 'output') {
        S.settings = r.settings;
        renderSettings();
        toast('저장 폴더를 바꿨어요.');
        return;
      }
      if (kind === 'folder' && !r.added && !r.skipped) toast('이 폴더에는 이미지가 없어요.');
      else afterAdd(r.added, r.skipped);
      pollNow();
    } catch (e) {
      toast(e.message, 'error');
    }
  }

  function afterAdd(added, skipped) {
    if (added) toast(`${num(added)}장을 넣었어요.`);
    if (skipped) toast(`${num(skipped)}개는 이미 목록에 있거나 지원하지 않는 파일이라 건너뛰었어요.`);
  }

  async function startOrStop() {
    if (S.queue.active) {
      try { await api('/api/cancel', {}); } catch (e) { toast(e.message, 'error'); }
      pollNow();
      return;
    }
    const ready = S.queue.items.some((i) => i.status === 'ready');
    try {
      await api('/api/start', ready ? {} : { redo: true });
      S.follow = true;
    } catch (e) {
      toast(e.message, 'error');
    }
    pollNow();
  }

  async function retry(id) {
    try {
      await api('/api/start', { ids: [id] });
      S.follow = true;
    } catch (e) {
      toast(e.message, 'error');
    }
    pollNow();
  }

  async function removeItem(id) {
    try { await api('/api/remove', { id }); } catch (e) { toast(e.message, 'error'); }
    pollNow();
  }

  async function openItem(id, target) {
    try { await api('/api/open', { id, target }); } catch (e) { toast(e.message, 'error'); }
  }

  // ───────── 끌어다 놓기·붙여넣기 ─────────
  async function collectEntries(entries) {
    const out = [];
    const readAll = (reader) => new Promise((res, rej) => reader.readEntries(res, rej));
    const fileOf = (entry) => new Promise((res, rej) => entry.file(res, rej));
    async function walk(entry, rel) {
      if (out.length >= 10000) return;
      if (entry.isFile) {
        try { out.push({ file: await fileOf(entry), rel }); } catch (_) { /* 읽을 수 없는 파일 */ }
      } else if (entry.isDirectory) {
        const dirRel = rel ? `${rel}/${entry.name}` : entry.name;
        const reader = entry.createReader();
        for (;;) {
          let batch = [];
          try { batch = await readAll(reader); } catch (_) { break; }
          if (!batch.length) break;
          for (const e of batch) await walk(e, dirRel);
        }
      }
    }
    for (const e of entries) await walk(e, '');
    return out;
  }

  async function uploadFiles(list) {
    const images = list.filter((x) => IMG_RE.test(x.file.name));
    const skipped = list.length - images.length;
    if (!images.length) {
      if (skipped) toast('지원하는 이미지가 없어요. PNG, JPG, WebP, BMP, TIF, GIF 파일을 넣어 주세요.', 'error');
      return;
    }
    S.uploading++;
    pollNow();
    let ok = 0;
    let fail = 0;
    let lastErr = '';
    let i = 0;
    const progress = images.length > 3 ? toast(`이미지를 넣는 중 0 / ${images.length}`, '', 0) : null;
    const worker = async () => {
      while (i < images.length) {
        const { file, rel } = images[i++];
        try {
          const res = await fetch(`/api/upload?name=${encodeURIComponent(file.name)}&rel=${encodeURIComponent(rel || '')}`, {
            method: 'POST', headers: { 'X-Token': TOKEN }, body: file,
          });
          if (res.ok) ok++;
          else {
            fail++;
            try { lastErr = (await res.json()).error || ''; } catch (_) { /* 무시 */ }
          }
        } catch (_) {
          fail++;
        }
        if (progress) progress.textContent = `이미지를 넣는 중 ${ok + fail} / ${images.length}`;
      }
    };
    await Promise.all([worker(), worker(), worker()]);
    if (progress) progress.remove();
    S.uploading--;
    if (ok) toast(`${num(ok)}장을 넣었어요.`);
    if (fail) toast(lastErr || `${num(fail)}장은 넣지 못했어요.`, 'error');
    if (skipped) toast(`이미지가 아닌 파일 ${num(skipped)}개는 건너뛰었어요.`);
    pollNow();
  }

  function setupDrop() {
    let depth = 0;
    const hasFiles = (e) => e.dataTransfer && Array.from(e.dataTransfer.types || []).includes('Files');
    const veil = (on) => {
      $('dropVeil').hidden = !on;
      $('stage').classList.toggle('is-dragging', on);
    };
    window.addEventListener('dragenter', (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth++;
      veil(true);
    });
    window.addEventListener('dragover', (e) => {
      e.preventDefault(); // 이게 없으면 창이 파일로 이동해 버립니다
      if (e.dataTransfer) e.dataTransfer.dropEffect = hasFiles(e) ? 'copy' : 'none';
    });
    window.addEventListener('dragleave', (e) => {
      if (!hasFiles(e)) return;
      depth = Math.max(0, depth - 1);
      if (!depth) veil(false);
    });
    window.addEventListener('drop', async (e) => {
      e.preventDefault();
      depth = 0;
      veil(false);
      const dt = e.dataTransfer;
      if (!dt) return;
      // DataTransfer 는 이벤트가 끝나면 비워지므로 항목을 먼저 꺼내 둡니다.
      const entries = Array.from(dt.items || [])
        .filter((it) => it.kind === 'file')
        .map((it) => (it.webkitGetAsEntry ? it.webkitGetAsEntry() : null))
        .filter(Boolean);
      const plain = Array.from(dt.files || []).map((file) => ({ file, rel: '' }));
      const list = entries.length ? await collectEntries(entries) : plain;
      uploadFiles(list);
    });
  }

  function stamp() {
    const d = new Date();
    const p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
  }

  function setupPaste() {
    document.addEventListener('paste', (e) => {
      const files = Array.from((e.clipboardData && e.clipboardData.files) || []).filter((f) => f.type.startsWith('image/'));
      if (!files.length) return;
      e.preventDefault();
      const named = files.map((f, i) => {
        const ext = ((f.type.split('/')[1] || 'png').replace('jpeg', 'jpg')).replace(/[^a-z0-9]/gi, '') || 'png';
        const generic = !f.name || /^image\.\w+$/i.test(f.name);
        const name = generic ? `붙여넣은 이미지 ${stamp()}${files.length > 1 ? `-${i + 1}` : ''}.${ext}` : f.name;
        return { file: new File([f], name, { type: f.type }), rel: '' };
      });
      uploadFiles(named);
    });
  }

  // ───────── 미리보기 조작 ─────────
  function setupViewer() {
    const vp = $('viewport');
    vp.addEventListener('wheel', (e) => {
      if (e.target.closest('.notice')) return;
      e.preventDefault();
      const r = vp.getBoundingClientRect();
      const f = Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015));
      zoomAt(f, e.clientX - r.left, e.clientY - r.top);
    }, { passive: false });

    let drag = null;
    vp.addEventListener('pointerdown', (e) => {
      if (e.button !== 0 || e.target.closest('.notice')) return;
      const onDivider = !!e.target.closest('.divider');
      vp.setPointerCapture(e.pointerId);
      drag = onDivider ? { type: 'div' } : { type: 'pan', x: e.clientX, y: e.clientY, tx: V.tx, ty: V.ty };
      if (!onDivider) vp.classList.add('is-panning');
      $('divider').classList.remove('is-entering');
      $('imgAfter').style.transition = '';
    });
    vp.addEventListener('pointermove', (e) => {
      if (!drag) return;
      if (drag.type === 'div') {
        const r = vp.getBoundingClientRect();
        V.div = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
        updateClip();
      } else {
        V.tx = drag.tx + (e.clientX - drag.x);
        V.ty = drag.ty + (e.clientY - drag.y);
        V.fit = false;
        applyTransform();
      }
    });
    const end = () => {
      drag = null;
      vp.classList.remove('is-panning');
    };
    vp.addEventListener('pointerup', end);
    vp.addEventListener('pointercancel', end);
    vp.addEventListener('dblclick', (e) => {
      if (e.target.closest('.notice, .divider')) return;
      const r = vp.getBoundingClientRect();
      if (!V.fit) fitView();
      else zoomAt(1 / V.s, e.clientX - r.left, e.clientY - r.top);
    });
    new ResizeObserver(() => {
      if (V.fit) fitView();
      else updateClip();
    }).observe(vp);

    $('zoomIn').onclick = () => zoomCenter(1.25);
    $('zoomOut').onclick = () => zoomCenter(0.8);
    $('zoomFit').onclick = fitView;
    $('zoom100').onclick = zoom100;
    $('pixelView').onchange = (e) => $('canvas').classList.toggle('is-pixel', e.target.checked);
    $('openResult').onclick = () => S.selected && openItem(S.selected, 'file');
    $('revealResult').onclick = () => S.selected && openItem(S.selected, 'folder');
  }

  // ───────── 모델 내려받기 창 ─────────
  function renderDownloads() {
    const d = S.download;
    const list = S.models.filter((m) => m.downloadable);
    $('dlList').innerHTML = list.map((m) => {
      let side;
      if (m.installed) {
        side = '<span class="dl-ok">설치됨</span>';
      } else if (d.state === 'running' && d.modelId === m.id) {
        const pct = d.total ? Math.floor((d.done / d.total) * 100) : 0;
        side = `<div>${pct}%</div><div class="meter"><span style="width:${pct}%"></span></div><button class="link-btn" data-dl-cancel type="button">취소</button>`;
      } else {
        const busy = d.state === 'running';
        side = `<button class="btn btn-small" data-dl="${esc(m.id)}" type="button" ${busy ? 'disabled' : ''}>내려받기</button><div style="margin-top:4px">${fmtBytes(m.size)}</div>`;
      }
      return `<li class="dl-item"><div class="dl-info"><div class="dl-name">${esc(m.name)}<span class="dl-sub">${esc(m.sub)}</span></div><p class="dl-desc">${esc(m.desc)}</p></div><div class="dl-side">${side}</div></li>`;
    }).join('');
  }

  // ───────── 이벤트 연결 ─────────
  function setupRadiogroupKeys() {
    document.addEventListener('keydown', (e) => {
      const group = e.target.closest && e.target.closest('[role="radiogroup"]');
      if (!group || !['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight'].includes(e.key)) return;
      const radios = Array.from(group.querySelectorAll('[role="radio"]'));
      const i = radios.indexOf(e.target.closest('[role="radio"]'));
      if (i < 0) return;
      e.preventDefault();
      e.stopPropagation();
      const d = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : -1;
      const next = radios[(i + d + radios.length) % radios.length];
      next.click();
      // 고르면 목록이 다시 그려지므로, 새로 선택된 항목에 포커스를 옮깁니다.
      const now = group.querySelector('[role="radio"][aria-checked="true"]') || next;
      now.focus();
    }, true);
  }

  function setupKeys() {
    document.addEventListener('keydown', (e) => {
      if (document.querySelector('dialog[open]')) return;
      const tag = (e.target.tagName || '').toLowerCase();
      if (['input', 'select', 'textarea'].includes(tag)) return;
      if (e.ctrlKey && e.key.toLowerCase() === 'o') {
        e.preventDefault();
        pick('images');
        return;
      }
      if (e.ctrlKey || e.altKey || e.metaKey || !S.queue.items.length) return;
      switch (e.key) {
        case 'ArrowRight': selectRel(1); break;
        case 'ArrowLeft': selectRel(-1); break;
        case 'Delete': if (S.selected) removeItem(S.selected); break;
        case '+': case '=': zoomCenter(1.25); break;
        case '-': zoomCenter(0.8); break;
        case '0': fitView(); break;
        case '1': zoom100(); break;
        default: return;
      }
      e.preventDefault();
    });
  }

  function bind() {
    $('pickImages').onclick = () => pick('images');
    $('pickFolder').onclick = () => pick('folder');
    $('emptyPick').onclick = () => pick('images');
    $('emptyFolder').onclick = () => pick('folder');
    $('tileAdd').onclick = () => pick('images');
    $('startBtn').onclick = startOrStop;
    $('clearDone').onclick = async () => { await api('/api/remove', { done: true }).catch((e) => toast(e.message, 'error')); pollNow(); };
    $('clearAll').onclick = async () => { await api('/api/remove', { all: true }).catch((e) => toast(e.message, 'error')); pollNow(); };

    $('modelList').addEventListener('click', (e) => {
      const row = e.target.closest('.model');
      if (row && row.dataset.id !== S.settings.model) patchSettings({ model: row.dataset.id });
    });
    $('scaleSeg').addEventListener('click', (e) => {
      const b = e.target.closest('button[data-scale]');
      if (b) patchSettings({ scale: Number(b.dataset.scale) });
    });
    $('formatSeg').addEventListener('click', (e) => {
      const b = e.target.closest('button[data-format]');
      if (b) patchSettings({ format: b.dataset.format });
    });
    $('webpQuality').addEventListener('input', (e) => patchSettings({ webpQuality: Number(e.target.value) }, true));
    for (const r of document.querySelectorAll('input[name="outmode"]')) {
      r.addEventListener('change', () => {
        if (r.value === 'custom' && !S.settings.outputDir) pick('output');
        else patchSettings({ outputMode: r.value });
      });
    }
    $('changeOut').onclick = () => pick('output');
    $('openOut').onclick = () => api('/api/open-dir', { which: 'output' }).catch((e) => toast(e.message, 'error'));

    // 고급 설정
    $('openSettings').onclick = () => $('settingsDlg').showModal();
    $('engineStatus').onclick = () => {
      $('settingsDlg').showModal();
      $('engineSection').scrollIntoView({ block: 'nearest' });
    };
    $('gpuSelect').onchange = (e) => patchSettings({ gpu: e.target.value });
    $('tileSelect').onchange = (e) => patchSettings({ tile: Number(e.target.value) });
    $('ttaCheck').onchange = (e) => patchSettings({ tta: e.target.checked });
    for (const r of document.querySelectorAll('input[name="uimode"]')) {
      r.addEventListener('change', () => {
        patchSettings({ uiMode: r.value });
        toast('다음에 프로그램을 실행할 때부터 적용돼요.');
      });
    }
    const probe = async () => {
      try {
        await api('/api/engine/probe', {});
        S.engine = { ...S.engine, state: 'checking', message: '엔진과 그래픽카드를 확인하는 중...' };
        renderEngine();
      } catch (e) {
        toast(e.message, 'error');
      }
      pollNow();
    };
    $('probeBtn').onclick = probe;
    $('bannerRetry').onclick = probe;
    const driverHelp = () => api('/api/open-url', { key: 'driver' }).catch((e) => toast(e.message, 'error'));
    $('driverBtn').onclick = driverHelp;
    $('bannerHelp').onclick = driverHelp;
    document.addEventListener('click', (e) => {
      const od = e.target.closest('[data-open-dir]');
      if (od) api('/api/open-dir', { which: od.dataset.openDir }).catch((err) => toast(err.message, 'error'));
      const ou = e.target.closest('[data-url]');
      if (ou) api('/api/open-url', { key: ou.dataset.url }).catch((err) => toast(err.message, 'error'));
    });
    $('aboutVersion').textContent = VERSION;

    // 모델 내려받기
    $('moreModels').onclick = () => {
      renderDownloads();
      $('modelsDlg').showModal();
    };
    $('dlList').addEventListener('click', async (e) => {
      const b = e.target.closest('[data-dl]');
      if (b) {
        try {
          const r = await api('/api/models/download', { id: b.dataset.dl });
          applyDownload(r.download);
        } catch (err) {
          toast(err.message, 'error');
        }
        pollNow();
      }
      if (e.target.closest('[data-dl-cancel]')) {
        await api('/api/models/cancel', {}).catch(() => {});
        pollNow();
      }
    });
    $('rescanBtn').onclick = async () => {
      try {
        const r = await api('/api/models/rescan', {});
        S.models = r.models;
        renderSettings();
        renderDownloads();
        toast('모델 목록을 새로 고쳤어요.');
      } catch (e) {
        toast(e.message, 'error');
      }
    };

    setupDrop();
    setupPaste();
    setupViewer();
    setupKeys();
    setupRadiogroupKeys();
  }

  // ───────── 시작 ─────────
  async function init() {
    let st;
    for (let tries = 0; ; tries++) {
      try {
        st = await api('/api/state');
        break;
      } catch (e) {
        if (tries > 20) {
          document.body.innerHTML = '<p style="padding:40px">프로그램과 연결하지 못했어요. 창을 닫고 다시 실행해 주세요.</p>';
          return;
        }
        await new Promise((r) => setTimeout(r, 500));
      }
    }
    S.settings = st.settings;
    S.models = st.models;
    S.engine = st.engine;
    S.download = st.download;
    lastDl = st.download.state;
    S.defaults = st.defaults;
    S.platform = st.platform;
    applyQueue(st.queue);
    if (st.queue.active) S.runStart = Date.now();
    bind();
    renderAll();
    poll();
    const ping = () => api('/api/ping', {}).catch(() => {});
    ping();
    setInterval(ping, 5000);
    window.addEventListener('pagehide', () => {
      try { navigator.sendBeacon(`/api/bye?token=${TOKEN}`); } catch (_) { /* 무시 */ }
    });
  }

  init();
})();
