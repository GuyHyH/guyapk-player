(() => {
'use strict';
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const el = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; };
const icon = id => { const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); s.setAttribute('class', 'ic'); const u = document.createElementNS('http://www.w3.org/2000/svg', 'use'); u.setAttribute('href', '#' + id); s.append(u); return s; };

const S = { settings: null, profiles: [], apps: [], env: {}, state: { phase: 'idle' }, view: 'play', kind: 'phone', accel: null, tuning: {}, search: '', pendingInstall: null, installing: false, provisioning: false };
let api, rt;

const t = (key, vars) => {
  const d = I18N[S.settings?.lang || 'th'];
  let s = d[key] ?? I18N.th[key] ?? key;
  if (vars) for (const k in vars) s = s.replace('{' + k + '}', vars[k]);
  return s;
};
const errText = e => {
  const c = String(e?.message ?? e ?? 'generic');
  const key = 'err.' + c;
  const d = I18N[S.settings?.lang || 'th'];
  if (d[key]) return d[key];
  if (c.startsWith('install:')) return t('err.install', { code: c.slice(8).replace(/^INSTALL_(FAILED_|PARSE_FAILED_)?/, '') });
  return t('err.generic');
};
const toast = (msg, bad) => {
  const n = el('div', 'toast' + (bad ? ' bad' : ''), msg);
  $('#toasts').append(n);
  setTimeout(() => { n.classList.add('out'); setTimeout(() => n.remove(), 260); }, bad ? 5200 : 2800);
};
const fmtSize = b => b >= 1e9 ? (b / 1e9).toFixed(2) + ' GB' : b >= 1e6 ? (b / 1e6).toFixed(1) + ' MB' : Math.round(b / 1e3) + ' KB';
const appInitial = a => [...(a.label || a.package || '?').trim()][0].toUpperCase();
const appIcon = (a, cls = '') => {
  const box = el('div', 'appicon ' + cls);
  if (a.icon && /^data:image\/(png|webp|jpeg);base64,/.test(a.icon)) {
    const img = new Image(); img.alt = ''; img.src = a.icon;
    img.onerror = () => { img.remove(); box.textContent = appInitial(a); };
    box.append(img);
  } else box.textContent = appInitial(a);
  return box;
};

const profile = () => {
  const s = S.settings;
  if (s.profile === 'custom') return { id: 'custom', kind: 'custom', w: Math.min(s.customW, s.customH), h: Math.max(s.customW, s.customH), dpi: s.customDpi };
  return S.profiles.find(p => p.id === s.profile) || S.profiles[3];
};
const profName = p => p.id === 'custom' ? t('custom.name') : (S.settings.lang === 'th' ? p.nameTh : p.nameEn);
const landscape = () => {
  const o = S.settings.orientation;
  if (S.state.phase === 'ready' && o === 'auto') return S.state.landscape;
  return o === 'landscape' || (o === 'auto' && S.state.landscape);
};

/* Rendering */
function applyText() {
  document.documentElement.lang = S.settings.lang;
  $$('[data-t]').forEach(n => { n.textContent = t(n.dataset.t); });
  $$('[data-ph]').forEach(n => { n.placeholder = t(n.dataset.ph); });
  $$('[data-tip]').forEach(n => { n.dataset.label = t(n.dataset.tip); n.setAttribute('aria-label', t(n.dataset.tip)); });
  $$('[data-lang]').forEach(b => b.classList.toggle('on', b.dataset.lang === S.settings.lang));
}
function renderKinds() {
  const icons = { phone: 'i-phone', tablet: 'i-tablet', foldable: 'i-fold', custom: 'i-custom' };
  const box = $('#kinds'); box.replaceChildren();
  for (const k of Object.keys(icons)) {
    const b = el('button', 'kind' + (S.kind === k ? ' on' : ''));
    b.append(icon(icons[k]), el('span', '', t('kind.' + k)));
    b.onclick = () => {
      S.kind = k;
      if (k === 'custom') save({ profile: 'custom' });
      else if (profile().kind !== k) save({ profile: S.profiles.find(p => p.kind === k).id });
      renderPlay();
    };
    box.append(b);
  }
}
function renderModels() {
  const list = $('#models'), custom = $('#custom');
  const isCustom = S.kind === 'custom';
  list.hidden = isCustom; custom.hidden = !isCustom;
  if (isCustom) {
    for (const [id, k] of [['cw', 'customW'], ['ch', 'customH'], ['cd', 'customDpi']]) if (document.activeElement !== $('#' + id)) $('#' + id).value = S.settings[k];
    return;
  }
  list.replaceChildren();
  const cur = profile();
  for (const p of S.profiles.filter(p => p.kind === S.kind)) {
    const b = el('button', 'model' + (p.id === cur.id ? ' on' : ''));
    const g = el('span', 'glyph'); const k = 24 / p.h; g.style.width = Math.max(9, p.w * k) + 'px'; g.style.height = '24px';
    const txt = el('span'); txt.append(el('b', '', profName(p)), el('small', '', `${p.w} × ${p.h} · ${p.dpi} dpi`));
    b.append(g, txt, el('span', 'inch', p.inch + '"'));
    b.onclick = () => { save({ profile: p.id }); renderPlay(); };
    list.append(b);
  }
}
function renderOrient() {
  const box = $('#orient'); box.replaceChildren();
  for (const [v, ic] of [['auto', 'i-auto'], ['portrait', 'i-portrait'], ['landscape', 'i-landscape']]) {
    const b = el('button', S.settings.orientation === v ? 'on' : '');
    b.append(icon(ic), el('span', '', t('orient.' + v)));
    b.setAttribute('aria-pressed', S.settings.orientation === v);
    b.onclick = async () => {
      S.settings.orientation = v; renderOrient(); layoutDevice();
      try { const l = await api.SetOrientation(v); S.state.landscape = l; layoutDevice(); } catch (e) { toast(errText(e), true); }
    };
    box.append(b);
  }
}
function renderPerf() {
  const box = $('#perf'); box.replaceChildren();
  for (const [v, ic] of [['eco', 'i-leaf'], ['balanced', 'i-scale'], ['max', 'i-bolt']]) {
    const b = el('button', S.settings.perf === v ? 'on' : '');
    const pi = el('span', 'pi'); pi.append(icon(ic));
    const tx = el('span'); tx.append(el('b', '', t('perf.' + v)));
    const u = S.tuning[v];
    tx.append(el('small', '', u ? `${u.cores} ${t('perf.cores')} · ${Math.round(u.ramMb / 1024 * 10) / 10} GB RAM` : ''));
    b.append(pi, tx);
    b.onclick = () => { save({ perf: v }); renderPerf(); };
    box.append(b);
  }
}
function renderRecent() {
  const box = $('#recent'); box.replaceChildren();
  const list = S.apps.slice(0, 4);
  if (!list.length) return;
  box.append(el('h2', '', t('recent.title')));
  const row = el('div', 'recent-row');
  for (const a of list) {
    const b = el('button', 'recent-app'); b.title = a.label;
    b.append(appIcon(a), el('span', '', a.label));
    b.onclick = () => launch(a.package);
    row.append(b);
  }
  box.append(row);
}
function layoutDevice() {
  const stage = $('#stage'); if (!stage.clientWidth) return;
  const p = profile(), land = landscape();
  const availW = stage.clientWidth - 56, availH = stage.clientHeight - 186;
  const dw = land ? p.h : p.w, dh = land ? p.w : p.h;
  const k = Math.min(availW / dw, availH / dh);
  const w = Math.round(p.w * k), h = Math.round(p.h * k);
  const dev = $('#device'), ui = $('#screenUi');
  const bz = Math.max(5, Math.round(Math.min(w, h) * .028));
  const rad = Math.round(Math.min(w, h) * (p.kind === 'tablet' ? .05 : .11)) + bz;
  dev.style.width = w + 'px'; dev.style.height = h + 'px';
  dev.style.setProperty('--rot', land ? '90deg' : '0deg');
  dev.style.setProperty('--bz', bz + 'px'); dev.style.setProperty('--rad', rad + 'px');
  const iw = w - bz * 2, ih = h - bz * 2;
  ui.style.width = (land ? ih : iw) + 'px'; ui.style.height = (land ? iw : ih) + 'px';
  ui.style.setProperty('--u', Math.max(1.7, Math.min(iw, ih, 400) / 100) + 'px');
}
function renderScreen() {
  const p = profile(), st = S.state, ready = S.env.ready;
  let msg = t('screen.idle');
  if (!ready) msg = t('screen.nosdk');
  else if (st.phase === 'booting') msg = t('step.' + (st.step || 'prepare'));
  else if (st.phase === 'stopping') msg = t('screen.stopping');
  else if (st.phase === 'ready') msg = t('screen.ready');
  $('#screenState').textContent = msg;
  $('#screenSpec').textContent = `${landscape() ? p.h + ' × ' + p.w : p.w + ' × ' + p.h}`;
  $('#capName').textContent = profName(p);
  $('#capSpec').textContent = `${p.dpi} dpi · ${t('orient.' + (landscape() ? 'landscape' : 'portrait'))}`;
  const bar = $('#bootbar'); bar.hidden = st.phase !== 'booting';
  bar.style.setProperty('--p', ({ prepare: 10, launch: 28, android: 62, tune: 92 })[st.step] + '%');
}
function renderStatus() {
  const st = S.state, ready = S.env.ready;
  const key = !ready ? 'nosdk' : st.phase;
  $('#status').className = 'status ' + key;
  $('#statusText').textContent = t('status.' + key);
  const b = document.body;
  b.classList.toggle('is-ready', st.phase === 'ready');
  b.classList.toggle('is-booting', st.phase === 'booting');
  b.classList.toggle('needs-restart', !!st.restart);
  $('#dock').hidden = st.phase !== 'ready';
  const cta = $('#cta');
  let txt = 'cta.start', dis = false;
  if (!ready) txt = 'cta.setup';
  else if (st.phase === 'booting') { txt = 'cta.booting'; dis = true; }
  else if (st.phase === 'stopping') { txt = 'cta.stopping'; dis = true; }
  else if (st.restart) txt = 'cta.restart';
  $('#ctaText').textContent = t(txt); cta.disabled = dis;
}
function renderPlay() { renderKinds(); renderModels(); renderOrient(); renderPerf(); renderRecent(); layoutDevice(); renderScreen(); renderStatus(); }
function renderLibrary() {
  const n = S.apps.length;
  const badge = $('#libCount'); badge.hidden = !n; badge.textContent = n;
  $('#libSub').textContent = n ? t('lib.count', { n }) : t('lib.none');
  const q = S.search.trim().toLowerCase();
  const list = S.apps.filter(a => !q || a.label.toLowerCase().includes(q) || a.package.toLowerCase().includes(q));
  const grid = $('#grid'); grid.replaceChildren();
  $('#libEmpty').hidden = n > 0; grid.hidden = n === 0;
  $('.lib-tools').style.visibility = n ? '' : 'hidden';
  if (n && !list.length) { const none = el('p', '', t('lib.nomatch')); none.style.color = 'var(--muted)'; grid.append(none); }
  for (const a of list) {
    const c = el('article', 'card-app');
    c.append(appIcon(a), el('b', '', a.label), el('small', '', [a.version && 'v' + a.version, a.package].filter(Boolean).join(' · ')));
    const seg = el('div', 'mini-seg');
    for (const [v, k] of [['', 'app.follow'], ['portrait', 'orient.portrait'], ['landscape', 'orient.landscape']]) {
      const b = el('button', (a.override || '') === v ? 'on' : '', t(k));
      b.onclick = async () => { try { await api.SetAppOrientation(a.package, v); a.override = v; renderLibrary(); } catch (e) { toast(errText(e), true); } };
      seg.append(b);
    }
    const foot = el('div', 'card-foot');
    const play = el('button', 'btn gold'); play.append(icon('i-play'), el('span', '', t('app.play'))); play.onclick = () => launch(a.package);
    const rm = el('button', 'btn rm'); rm.append(icon('i-trash')); rm.title = t('app.remove'); rm.setAttribute('aria-label', t('app.remove')); rm.onclick = () => removeApp(a);
    foot.append(play, rm);
    c.append(seg, foot); grid.append(c);
  }
}
function renderSettings() {
  $('#setAuto').checked = !!S.settings.autoLaunch;
  const e = S.env, box = $('#facts'); box.replaceChildren();
  const fact = (k, v, cls, full) => { const d = el('div', full ? 'full' : ''); d.append(el('dt', '', t(k)), el('dd', cls, v)); box.append(d); };
  const osName = { windows: 'Windows', darwin: 'macOS' }[e.os] || e.os;
  fact('fact.os', `${osName} (${e.arch})`);
  fact('fact.cpu', String(e.threads));
  fact('fact.ram', e.ramGb + ' GB');
  fact('fact.accel', S.accel === null ? t('fact.checking') : (S.accel || t('fact.none')), S.accel ? 'good' : S.accel === '' ? 'bad' : '');
  fact('fact.android', e.api ? `Android API ${e.api}` : t('fact.notInstalled'));
  fact('fact.abi', e.abi);
  fact('fact.sdk', e.ready ? e.sdkPath : t('fact.notInstalled'), '', true);
  $('#dataPath').textContent = e.dataPath;
  $('#about').textContent = t('about', { v: e.version });
}
function renderRemote() {
  const r = S.remote || {};
  $('#rmToggle').checked = !!r.enabled;
  $('#rmOff').hidden = !!r.enabled; $('#rmOn').hidden = !r.enabled;
  $('#rmDot').hidden = !r.connected;
  if (r.enabled) {
    if ($('#rmQR').getAttribute('src') !== r.qr) $('#rmQR').src = r.qr;
    $('#rmURL').textContent = r.setupUrl;
    $('#rmPin').textContent = r.pin;
    renderRemoteExpiry();
  }
  $('#rmConn').classList.toggle('on', !!r.connected);
  $('#rmConnText').textContent = t(r.connected ? 'rm.connected' : r.enabled ? 'rm.waiting' : 'rm.disabled');
  $('#rmPaired').textContent = t('rm.paired', { n: r.paired || 0 });
  $('#rmUnpair').disabled = !r.paired;
}
function renderRemoteExpiry() {
  const r = S.remote; if (!r?.enabled) return;
  const left = Math.max(0, r.pinExpires - Math.floor(Date.now() / 1000));
  $('#rmExpire').textContent = t('rm.expire', { t: Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0') });
  if (left === 0 && !S.remoteRefreshing) {
    S.remoteRefreshing = true;
    api.RemoteInfo().then(i => { S.remote = i; renderRemote(); }).finally(() => { S.remoteRefreshing = false; });
  }
}
function renderAll() { applyText(); renderPlay(); renderLibrary(); renderSettings(); renderRemote(); }
function setView(v) {
  S.view = v;
  $$('.tab').forEach(b => { const on = b.dataset.view === v; b.classList.toggle('on', on); b.setAttribute('aria-selected', on); });
  for (const id of ['play', 'library', 'remote', 'settings']) $('#view-' + id).hidden = id !== v;
  if (v === 'play') requestAnimationFrame(layoutDevice);
}

/* Actions */
let saveTimer;
function save(patch) {
  Object.assign(S.settings, patch);
  clearTimeout(saveTimer);
  saveTimer = setTimeout(async () => {
    try { await api.SaveSettings(S.settings); if (S.state.restart) toast(t('toast.restart')); } catch (e) { toast(errText(e), true); }
  }, 120);
}
async function primary() {
  if (!S.env.ready) return openSetup();
  try { await flushSave(); await api.StartDevice(); toast(t('toast.ready')); }
  catch (e) { if (String(e) === 'no_sdk') openSetup(); else toast(errText(e), true); }
}
async function flushSave() { clearTimeout(saveTimer); await api.SaveSettings(S.settings); }
async function launch(pkg) {
  if (!S.env.ready) return openSetup();
  try { await flushSave(); await api.Launch(pkg); S.apps = await api.Apps(); renderRecent(); renderLibrary(); }
  catch (e) { toast(errText(e), true); }
}
function confirmBox(title, text, yes) {
  return new Promise(res => {
    $('#cTitle').textContent = title; $('#cText').textContent = text; $('#cYes').textContent = yes;
    const m = $('#confirmModal'); m.hidden = false; $('#cNo').focus();
    const done = v => { m.hidden = true; $('#cNo').onclick = $('#cYes').onclick = null; res(v); };
    $('#cNo').onclick = () => done(false); $('#cYes').onclick = () => done(true);
  });
}
async function removeApp(a) {
  if (!await confirmBox(t('confirm.removeTitle', { name: a.label }), t('confirm.removeText'), t('common.delete'))) return;
  try { await api.Uninstall(a.package); S.apps = await api.Apps(); renderRecent(); renderLibrary(); toast(t('toast.removed')); }
  catch (e) { toast(errText(e), true); }
}
async function pickAndInstall() {
  try { const f = await api.PickPackage(); if (f) beginInstall(f); } catch (e) { toast(errText(e), true); }
}

const STEPS = ['read', 'boot', 'install', 'launch'];
function drawSteps(active, failed) {
  const list = S.settings.autoLaunch ? STEPS : STEPS.slice(0, 3);
  const idx = active === 'done' ? list.length : list.indexOf(active);
  const box = $('#mSteps'); box.replaceChildren();
  list.forEach((k, i) => {
    const li = el('li', i < idx ? 'done' : i === idx && !failed ? 'active' : '');
    const dot = el('i'); dot.append(icon('i-check'));
    li.append(dot, el('span', '', t('istep.' + k)));
    box.append(li);
  });
}
async function beginInstall(path) {
  if (S.installing) return;
  if (!S.env.ready) { S.pendingInstall = path; return openSetup(); }
  const m = $('#installModal'), go = $('#mGo'), err = $('#mErr');
  err.hidden = true; $('#mSteps').replaceChildren(); $('#mChips').replaceChildren();
  $('#mIcon').replaceChildren(); $('#mIcon').className = 'pkg-icon';
  $('#mLabel').textContent = path.split(/[\\/]/).pop(); $('#mMeta').textContent = '';
  go.textContent = t(S.settings.autoLaunch ? 'install.go' : 'install.goOnly'); go.disabled = true;
  $('#mCancel').textContent = t('common.cancel'); $('#mCancel').disabled = false;
  $('#mCancel').onclick = () => { m.hidden = true; };
  m.hidden = false;
  let info;
  try { info = await api.Inspect(path); }
  catch (e) { err.textContent = errText(e); err.hidden = false; $('#mCancel').textContent = t('install.close'); $('#mCancel').onclick = () => { m.hidden = true; }; return; }
  const ic = appIcon({ icon: info.icon, label: info.label }, 'pkg-icon');
  $('#mIcon').replaceWith(ic); ic.id = 'mIcon';
  $('#mLabel').textContent = info.label;
  $('#mMeta').textContent = [info.versionName && 'v' + info.versionName, info.package, fmtSize(info.size)].filter(Boolean).join(' · ');
  const chips = [];
  if (info.bundle) chips.push(t('chip.bundle'));
  chips.push(info.orientation ? t('chip.' + info.orientation) : t('chip.anyOrient'));
  if (info.minSdk) chips.push('Android API ' + info.minSdk + '+');
  chips.push(info.abis?.length ? info.abis.join(', ') : t('chip.universal'));
  $('#mChips').replaceChildren(...chips.map(c => el('span', '', c)));
  drawSteps('');
  go.disabled = false;
  $('#mCancel').onclick = () => { m.hidden = true; };
  go.onclick = () => runInstall(path);
}
async function runInstall(path) {
  const m = $('#installModal'), go = $('#mGo'), err = $('#mErr');
  S.installing = true; err.hidden = true; go.disabled = true; $('#mCancel').disabled = true;
  S.installStep = 'read'; drawSteps('read');
  try {
    await flushSave();
    const a = await api.Install(path, !!S.settings.autoLaunch);
    S.apps = await api.Apps(); renderRecent(); renderLibrary();
    drawSteps('done');
    toast(t('toast.installed', { name: a.label }));
    setTimeout(() => { m.hidden = true; }, 650);
  } catch (e) {
    S.apps = await api.Apps(); renderRecent(); renderLibrary();
    drawSteps(S.installStep, true);
    err.textContent = errText(e); err.hidden = false;
    go.textContent = t('install.retry'); go.disabled = false;
    $('#mCancel').textContent = t('install.close'); $('#mCancel').disabled = false;
  } finally { S.installing = false; }
}

/* Runtime setup */
async function openSetup() {
  const m = $('#setupModal');
  m.hidden = false;
  $('#setupBody').hidden = false; $('#setupProgress').hidden = true; $('#setupErr').hidden = true;
  $('#setupAgree').checked = false; $('#setupGo').disabled = true;
  $('#setupGo').textContent = t('setup.go'); $('#setupLater').textContent = t('setup.later');
  const list = $('#setupPkgs'); list.replaceChildren(el('li', '', t('setup.loading')));
  $('#setupLicense').textContent = '';
  try {
    const plan = await api.RuntimePlan();
    list.replaceChildren();
    for (const p of plan.packages) {
      const li = el('li');
      const m2 = /^system-images;android-(\d+)/.exec(p.path);
      li.append(el('span', '', m2 ? t('pkg.image', { api: m2[1] }) : t('pkg.' + p.path)), el('span', '', fmtSize(p.size)));
      list.append(li);
    }
    const tot = el('li', 'total'); tot.append(el('span', '', t('setup.total')), el('span', '', fmtSize(plan.total))); list.append(tot);
    $('#setupLicense').textContent = plan.license;
  } catch (e) { list.replaceChildren(); $('#setupErr').textContent = errText(e); $('#setupErr').hidden = false; }
}
async function runSetup() {
  if (S.provisioning) return api.CancelRuntime();
  const go = $('#setupGo');
  S.provisioning = true;
  $('#setupBody').hidden = true; $('#setupProgress').hidden = false; $('#setupErr').hidden = true;
  $('#setupLater').hidden = true; go.textContent = t('setup.cancel');
  try {
    await api.InstallRuntime($('#setupAgree').checked);
    const boot = await api.Bootstrap(); S.env = boot.env; S.accel = await api.CheckAccel();
    $('#setupModal').hidden = true; renderAll();
    if (S.pendingInstall) { const f = S.pendingInstall; S.pendingInstall = null; beginInstall(f); }
  } catch (e) {
    $('#setupErr').textContent = errText(e); $('#setupErr').hidden = false;
    $('#setupBody').hidden = true; go.textContent = t('setup.go');
  } finally { S.provisioning = false; $('#setupLater').hidden = false; }
}
function onProvision(p) {
  const pct = p.total ? Math.min(100, p.done / p.total * 100) : 0;
  $('#setupBar').style.width = pct + '%';
  $('#setupPct').textContent = `${fmtSize(p.done)} / ${fmtSize(p.total)} · ${pct.toFixed(0)}%`;
  $('#setupStage').textContent = t('setup.' + p.stage);
}

/* Wiring */
function wire() {
  $$('.tab').forEach(b => b.onclick = () => setView(b.dataset.view));
  $$('[data-lang]').forEach(b => b.onclick = () => { save({ lang: b.dataset.lang }); renderAll(); });
  $('#cta').onclick = primary;
  $('#drop').onclick = pickAndInstall; $('#libAdd').onclick = pickAndInstall; $('#libEmptyAdd').onclick = pickAndInstall;
  $('#libSearch').oninput = e => { S.search = e.target.value; renderLibrary(); };
  $('#setAuto').onchange = e => save({ autoLaunch: e.target.checked });
  $('#openData').onclick = () => api.OpenDataFolder().catch(e => toast(errText(e), true));
  $('#setupAgree').onchange = e => { $('#setupGo').disabled = !e.target.checked; };
  $('#setupGo').onclick = runSetup;
  $('#setupLater').onclick = () => { $('#setupModal').hidden = true; S.pendingInstall = null; };
  $$('[data-key]').forEach(b => b.onclick = () => api.Key(b.dataset.key).catch(e => toast(errText(e), true)));
  $('#dRotate').onclick = async () => {
    try { const l = await api.Rotate(); S.state.landscape = l; if (S.settings.orientation !== 'auto') { S.settings.orientation = l ? 'landscape' : 'portrait'; renderOrient(); save({}); } layoutDevice(); renderScreen(); toast(t(l ? 'toast.rotated.l' : 'toast.rotated.p')); }
    catch (e) { toast(errText(e), true); }
  };
  $('#dShot').onclick = async () => { try { await api.Screenshot(); toast(t('toast.shot')); } catch (e) { toast(errText(e), true); } };
  $('#dStop').onclick = () => api.StopDevice().catch(e => toast(errText(e), true));

  for (const id of ['cw', 'ch', 'cd']) $('#' + id).oninput = () => {
    const w = +$('#cw').value, h = +$('#ch').value, d = +$('#cd').value;
    const lo = Math.min(w, h), hi = Math.max(w, h);
    const ok = lo >= 320 && hi >= 480 && hi <= 4096 && d >= 120 && d <= 640;
    $('#customHint').classList.toggle('bad', !ok);
    if (ok) { save({ customW: w, customH: h, customDpi: d }); layoutDevice(); renderScreen(); }
  };

  $('#rmToggle').onchange = async e => {
    const on = e.target.checked; e.target.disabled = true;
    try { S.remote = await api.RemoteEnable(on); } catch (err) { toast(errText(err), true); }
    e.target.disabled = false; renderRemote();
  };
  $('#rmNew').onclick = async () => { S.remote = await api.RemoteNewPIN(); renderRemote(); };
  $('#rmUnpair').onclick = async () => {
    if (!await confirmBox(t('rm.unpairTitle'), t('rm.unpairText'), t('rm.unpairYes'))) return;
    try { S.remote = await api.RemoteUnpairAll(); renderRemote(); } catch (err) { toast(errText(err), true); }
  };
  setInterval(() => { if (S.view === 'remote') renderRemoteExpiry(); }, 1000);

  $('#winMin').onclick = () => rt?.WindowMinimise();
  $('#winMax').onclick = () => rt?.WindowToggleMaximise();
  $('#winClose').onclick = () => rt?.Quit();

  let depth = 0;
  const veil = $('#dragveil');
  const hasFiles = e => [...(e.dataTransfer?.types || [])].includes('Files');
  addEventListener('dragenter', e => { if (hasFiles(e)) { depth++; veil.hidden = false; } });
  addEventListener('dragleave', e => { if (hasFiles(e) && --depth <= 0) { depth = 0; veil.hidden = true; } });
  addEventListener('dragover', e => e.preventDefault());
  addEventListener('drop', e => { e.preventDefault(); depth = 0; veil.hidden = true; });
  addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'o') { e.preventDefault(); pickAndInstall(); }
    if (e.key === 'Escape') { for (const id of ['confirmModal']) if (!$('#' + id).hidden) $('#cNo').click(); }
  });
  document.addEventListener('visibilitychange', () => document.body.classList.toggle('is-hidden', document.hidden));
  new ResizeObserver(layoutDevice).observe($('#stage'));

  const tick = () => { const d = new Date(); $('#clock').textContent = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }); };
  tick(); setInterval(tick, 15000);

  if (rt) {
    rt.EventsOn('state', s => { const was = S.state.phase; S.state = s; layoutDevice(); renderScreen(); renderStatus(); if (s.error && was !== 'idle') toast(errText(s.error), true); });
    rt.EventsOn('install', step => { S.installStep = step; if (!$('#installModal').hidden && step !== 'done') drawSteps(step); });
    rt.EventsOn('provision', onProvision);
    rt.EventsOn('file', beginInstall);
    rt.EventsOn('remote', i => { S.remote = i; renderRemote(); });
    rt.OnFileDrop?.((x, y, paths) => {
      const file = (paths || []).find(p => /\.(apk|xapk|apks|apkm)$/i.test(p));
      if (file) beginInstall(file); else toast(t('err.not_apk'), true);
    }, false);
  }
}

async function init() {
  for (let i = 0; i < 200 && !window.go?.main?.App; i++) await new Promise(r => setTimeout(r, 25));
  api = window.go.main.App; rt = window.runtime;
  const b = await api.Bootstrap();
  Object.assign(S, { settings: b.settings, profiles: b.profiles, apps: b.apps || [], env: b.env, state: b.state });
  S.kind = profile().kind;
  document.body.classList.toggle('mac', b.env.os === 'darwin');
  wire();
  const modes = ['eco', 'balanced', 'max'];
  (await Promise.all(modes.map(m => api.Tuning(m)))).forEach((u, i) => { S.tuning[modes[i]] = u; });
  S.remote = await api.RemoteInfo();
  renderAll(); setView('play');
  requestAnimationFrame(() => requestAnimationFrame(() => document.body.classList.remove('boot')));
  if (b.pending) beginInstall(b.pending);
  else if (!b.env.ready) openSetup();
  api.CheckAccel().then(a => { S.accel = a || ''; renderSettings(); });
}
init();
})();
