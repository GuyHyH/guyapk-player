(() => {
'use strict';
const T = {
  th: {
    installTitle: 'ติดตั้งลงหน้าจอหลัก', installSub: 'ใช้งานเต็มจอเหมือนแอปทั่วไป', install1: 'แตะปุ่มแชร์ของ Safari', install2: 'เลือก "เพิ่มไปยังหน้าจอโฮม"', install3: 'เปิด GuyAPK จากไอคอนบนหน้าจอหลัก', installSkip: 'ใช้งานในเบราว์เซอร์ต่อ',
    pairTitle: 'ใส่รหัส PIN', pairSub: 'ดูรหัส 6 หลักได้ที่แท็บ iPad ใน GuyAPK บนคอมพิวเตอร์',
    wrong: 'รหัสไม่ถูกต้อง', locked: 'ใส่ผิดหลายครั้ง รอ 1 นาทีแล้วใช้รหัสใหม่', expired: 'รหัสหมดอายุ ดูรหัสใหม่บนคอมพิวเตอร์', netErr: 'เชื่อมต่อคอมพิวเตอร์ไม่ได้',
    appsTitle: 'แอปของฉัน', noApps: 'ยังไม่มีแอป ติดตั้ง APK จาก GuyAPK บนคอมพิวเตอร์ก่อน',
    back: 'ย้อนกลับ', home: 'หน้าหลัก', recents: 'แอปล่าสุด', rotate: 'หมุนจอ', apps: 'แอปของฉัน', hide: 'ซ่อนแถบควบคุม', show: 'แสดงแถบควบคุม', close: 'ปิด',
    idle: 'เครื่อง Android ยังไม่เปิด', start: 'เปิดเครื่อง', booting: 'กำลังเปิดเครื่อง Android…', stopping: 'กำลังปิดเครื่อง…', linking: 'กำลังเชื่อมต่อภาพ…', reconnect: 'กำลังเชื่อมต่อใหม่…',
    replaced: 'กำลังใช้งานบนอุปกรณ์อื่น', useHere: 'ใช้งานบน iPad เครื่องนี้', stream: 'เริ่มส่งภาพไม่สำเร็จ กำลังลองใหม่…', noCodec: 'iPad เครื่องนี้ไม่รองรับการถอดรหัสวิดีโอ กรุณาอัปเดตเป็น iPadOS 16.4 ขึ้นไป', actionErr: 'ทำรายการไม่สำเร็จ',
  },
  en: {
    installTitle: 'Add to Home Screen', installSub: 'Runs full screen like a regular app', install1: 'Tap Safari’s Share button', install2: 'Choose "Add to Home Screen"', install3: 'Open GuyAPK from your Home Screen', installSkip: 'Continue in browser',
    pairTitle: 'Enter PIN', pairSub: 'Find the 6-digit code on the iPad tab of GuyAPK on your computer',
    wrong: 'Incorrect PIN', locked: 'Too many attempts. Wait 1 minute and use the new PIN.', expired: 'PIN expired. Check the new one on your computer.', netErr: 'Could not reach your computer',
    appsTitle: 'My apps', noApps: 'No apps yet. Install an APK from GuyAPK on your computer first.',
    back: 'Back', home: 'Home', recents: 'Recent apps', rotate: 'Rotate', apps: 'My apps', hide: 'Hide controls', show: 'Show controls', close: 'Close',
    idle: 'Android is not running', start: 'Start device', booting: 'Starting Android…', stopping: 'Shutting down…', linking: 'Connecting video…', reconnect: 'Reconnecting…',
    replaced: 'In use on another device', useHere: 'Use on this iPad', stream: 'Could not start video. Retrying…', noCodec: 'This iPad cannot decode the video. Please update to iPadOS 16.4 or later.', actionErr: 'That action failed',
  },
};
const $ = s => document.querySelector(s);
const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch {} },
};
let lang = store.get('guy.lang') === 'en' ? 'en' : 'th';
const t = k => T[lang][k] || k;
const stats = { frames: 0, packets: 0, width: 0, height: 0, decoderErrors: 0, phase: '' };
window.GuyRemote = { stats };

function applyText() {
  document.documentElement.lang = lang;
  document.querySelectorAll('[data-t]').forEach(n => { n.textContent = t(n.dataset.t); });
  document.querySelectorAll('[data-l]').forEach(n => n.setAttribute('aria-label', t(n.dataset.l)));
  $('#lang').textContent = lang === 'th' ? 'EN' : 'ไทย';
  renderOverlay();
}
let toastTimer;
function toast(msg, bad) {
  const n = $('#toast'); n.textContent = msg; n.className = bad ? 'bad' : ''; n.hidden = false;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => { n.hidden = true; }, bad ? 4500 : 2200);
}
const show = id => { for (const s of ['install', 'pair', 'player']) $('#' + s).hidden = s !== id; };

async function api(path, body) {
  const res = await fetch(path, body === undefined ? { cache: 'no-store' } : {
    method: 'POST', cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) { const e = new Error(data.error || String(res.status)); e.status = res.status; throw e; }
  return data;
}

/* Pairing */
let pin = '';
function drawDots() {
  const d = $('#dots'); d.replaceChildren();
  for (let i = 0; i < 6; i++) { const n = document.createElement('i'); if (i < pin.length) n.className = 'on'; d.append(n); }
}
function buildPad() {
  const pad = $('#pad');
  for (const k of ['1', '2', '3', '4', '5', '6', '7', '8', '9', '', '0', 'del']) {
    const b = document.createElement('button');
    if (k === '') { b.className = 'ghost'; b.disabled = true; b.setAttribute('aria-hidden', 'true'); }
    else if (k === 'del') { b.className = 'ghost'; b.setAttribute('aria-label', 'Delete'); b.innerHTML = '<svg class="ic"><use href="#i-del"/></svg>'; b.onclick = () => { pin = pin.slice(0, -1); drawDots(); }; }
    else { b.textContent = k; b.onclick = () => press(k); }
    pad.append(b);
  }
}
async function press(k) {
  if (pin.length >= 6) return;
  pin += k; drawDots(); $('#pairErr').hidden = true;
  if (pin.length < 6) return;
  try { await api('/api/pair', { pin }); pin = ''; drawDots(); enterPlayer(); }
  catch (e) {
    pin = ''; drawDots();
    const code = ['wrong', 'locked', 'expired'].includes(e.message) ? e.message : 'netErr';
    $('#pairErr').textContent = t(code); $('#pairErr').hidden = false;
    const d = $('#dots'); d.classList.remove('bad'); void d.offsetWidth; d.classList.add('bad');
  }
}

/* Video */
const cv = $('#screen');
const ctx2d = cv.getContext('2d', { alpha: false, desynchronized: true });
let ws = null, dec = null, cfg = null, waitKey = true, latest = null, raf = 0, vw = 0, vh = 0, showing = false;
let state = { phase: '' }, overlayMode = 'linking', retry = 0, retryTimer = 0, replaced = false;

function renderOverlay() {
  const text = $('#overlayText'), btn = $('#overlayBtn'), bar = $('#overlayBar');
  if (showing) { $('#overlay').hidden = true; return; }
  $('#overlay').hidden = false;
  btn.hidden = true; bar.hidden = true;
  if (typeof VideoDecoder === 'undefined') { text.textContent = t('noCodec'); return; }
  if (overlayMode === 'replaced') { text.textContent = t('replaced'); btn.textContent = t('useHere'); btn.hidden = false; return; }
  if (overlayMode === 'idle') { text.textContent = t('idle'); btn.textContent = t('start'); btn.hidden = false; return; }
  text.textContent = t(overlayMode); bar.hidden = false;
}
function setOverlay(mode) { overlayMode = mode; showing = false; renderOverlay(); }

function avcCodec(b) {
  for (let i = 0; i + 7 < b.length; i++) {
    if (b[i] === 0 && b[i + 1] === 0 && b[i + 2] === 1 && (b[i + 3] & 31) === 7) {
      return 'avc1.' + [b[i + 4], b[i + 5], b[i + 6]].map(x => x.toString(16).padStart(2, '0')).join('');
    }
  }
  return 'avc1.42e033';
}
function newDecoder() {
  if (dec && dec.state !== 'closed') { try { dec.close(); } catch {} }
  dec = new VideoDecoder({
    output(frame) {
      if (latest) latest.close();
      latest = frame; stats.frames++;
      if (!raf) raf = requestAnimationFrame(paint);
    },
    error() {
      stats.decoderErrors++; waitKey = true; dec = null;
      if (cfg) { configure(); requestKey(); }
    },
  });
}
function configure() {
  if (!dec || dec.state === 'closed') newDecoder();
  dec.configure({ codec: avcCodec(cfg), optimizeForLatency: true, hardwareAcceleration: 'prefer-hardware' });
  waitKey = true;
}
function paint() {
  raf = 0;
  if (!latest) return;
  ctx2d.drawImage(latest, 0, 0, cv.width, cv.height);
  latest.close(); latest = null;
  if (!showing) { showing = true; renderOverlay(); }
}
function requestKey() { if (ws && ws.readyState === 1) ws.send('{"t":"key"}'); }
function fit() {
  if (!vw) return;
  const s = $('#stage'), cs = getComputedStyle(s);
  const aw = s.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  const ah = s.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
  const k = Math.min(aw / vw, ah / vh);
  cv.style.width = Math.floor(vw * k) + 'px'; cv.style.height = Math.floor(vh * k) + 'px';
}
function setSize(w, h) {
  vw = w; vh = h; stats.width = w; stats.height = h;
  if (cv.width !== w || cv.height !== h) { cv.width = w; cv.height = h; }
  fit();
}
function onBinary(buf) {
  const u = new Uint8Array(buf), dv = new DataView(buf), k = u[0];
  stats.packets++;
  if (k === 0) { setSize(dv.getUint16(1), dv.getUint16(3)); return; }
  if (k === 1) { cfg = u.slice(1); configure(); return; }
  if (!dec || dec.state !== 'configured') return;
  const pts = Number(dv.getBigUint64(1)), data = u.subarray(9);
  if (k === 2) {
    // Without an avcC description, parameter sets must travel with the key frame.
    const merged = new Uint8Array(cfg.length + data.length);
    merged.set(cfg); merged.set(data, cfg.length);
    waitKey = false;
    dec.decode(new EncodedVideoChunk({ type: 'key', timestamp: pts, data: merged }));
  } else if (!waitKey) {
    if (dec.decodeQueueSize > 5) { waitKey = true; requestKey(); return; }
    dec.decode(new EncodedVideoChunk({ type: 'delta', timestamp: pts, data }));
  }
}
function onState(m) {
  state = m; stats.phase = m.phase;
  if (m.phase === 'ready') { if (!showing) setOverlay(m.error === 'stream' ? 'stream' : 'linking'); }
  else setOverlay(m.phase === 'idle' ? 'idle' : m.phase === 'stopping' ? 'stopping' : 'booting');
}
function connect() {
  if (ws || document.hidden || typeof VideoDecoder === 'undefined') { renderOverlay(); return; }
  clearTimeout(retryTimer);
  const s = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws');
  s.binaryType = 'arraybuffer'; ws = s;
  s.onopen = () => { retry = 0; replaced = false; };
  s.onmessage = e => {
    if (typeof e.data === 'string') { try { const m = JSON.parse(e.data); if (m.t === 'state') onState(m); } catch {} }
    else onBinary(e.data);
  };
  s.onclose = async e => {
    if (ws === s) ws = null;
    releaseAll();
    if (e.code === 4001) { replaced = true; setOverlay('replaced'); return; }
    if (document.hidden || replaced) return;
    setOverlay('reconnect');
    try { if (!(await api('/api/me')).paired) { show('pair'); return; } } catch {}
    retry = Math.min(retry + 1, 5);
    retryTimer = setTimeout(connect, 400 * retry);
  };
}
function disconnect() { clearTimeout(retryTimer); if (ws) { const s = ws; ws = null; s.onclose = null; s.close(); } releaseAll(); showing = false; }

/* Touch */
const slots = new Map();
function encodeTouch(action, slot, e) {
  const r = cv.getBoundingClientRect();
  const nx = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
  const ny = Math.max(0, Math.min(1, (e.clientY - r.top) / r.height));
  const b = new DataView(new ArrayBuffer(6));
  b.setUint8(0, action); b.setUint8(1, slot);
  b.setUint16(2, Math.round(nx * 65535)); b.setUint16(4, Math.round(ny * 65535));
  return b.buffer;
}
function sendTouch(action, slot, e) { if (ws && ws.readyState === 1) ws.send(encodeTouch(action, slot, e)); }
function releaseAll() { slots.clear(); }
cv.addEventListener('pointerdown', e => {
  e.preventDefault();
  let slot = 0; const used = new Set(slots.values());
  while (used.has(slot) && slot < 10) slot++;
  if (slot >= 10) return;
  slots.set(e.pointerId, slot);
  try { cv.setPointerCapture(e.pointerId); } catch {}
  sendTouch(0, slot, e);
});
cv.addEventListener('pointermove', e => {
  const slot = slots.get(e.pointerId);
  if (slot === undefined) return;
  e.preventDefault();
  // Drop intermediate moves when the network is behind, to keep touch responsive.
  if (ws && ws.bufferedAmount > 2048) return;
  sendTouch(2, slot, e);
});
for (const [type, action] of [['pointerup', 1], ['pointercancel', 3]]) {
  cv.addEventListener(type, e => {
    const slot = slots.get(e.pointerId);
    if (slot === undefined) return;
    e.preventDefault(); slots.delete(e.pointerId);
    sendTouch(action, slot, e);
  });
}
for (const ev of ['gesturestart', 'gesturechange', 'contextmenu']) document.addEventListener(ev, e => e.preventDefault());
cv.addEventListener('touchmove', e => e.preventDefault(), { passive: false });

/* Controls */
document.querySelectorAll('[data-key]').forEach(b => {
  b.onclick = () => api('/api/key', { name: b.dataset.key }).catch(() => toast(t('actionErr'), true));
});
$('#rotate').onclick = () => api('/api/rotate', {}).catch(() => toast(t('actionErr'), true));
$('#lang').onclick = () => { lang = lang === 'th' ? 'en' : 'th'; store.set('guy.lang', lang); applyText(); };
$('#hideBar').onclick = () => { $('#bar').hidden = true; $('#showBar').hidden = false; requestAnimationFrame(fit); };
$('#showBar').onclick = () => { $('#bar').hidden = false; $('#showBar').hidden = true; requestAnimationFrame(fit); };
$('#overlayBtn').onclick = async () => {
  if (overlayMode === 'replaced') { replaced = false; setOverlay('linking'); connect(); return; }
  try { await api('/api/start', {}); setOverlay('booting'); } catch { toast(t('actionErr'), true); }
};
$('#closeDrawer').onclick = () => { $('#drawer').hidden = true; };
$('#drawer').onclick = e => { if (e.target.id === 'drawer') $('#drawer').hidden = true; };
$('#apps').onclick = async () => {
  const grid = $('#grid'); grid.replaceChildren(); $('#drawer').hidden = false;
  let list = [];
  try { list = await api('/api/apps'); } catch { toast(t('actionErr'), true); }
  if (!list || !list.length) { const n = document.createElement('p'); n.className = 'none'; n.textContent = t('noApps'); grid.append(n); return; }
  for (const a of list) {
    const b = document.createElement('button'), box = document.createElement('div'), name = document.createElement('span');
    box.className = 'appicon'; name.textContent = a.label;
    if (a.icon && /^data:image\/(png|webp|jpeg);base64,/.test(a.icon)) { const i = new Image(); i.alt = ''; i.src = a.icon; box.append(i); }
    else box.textContent = [...(a.label || '?')][0].toUpperCase();
    b.append(box, name);
    b.onclick = () => { $('#drawer').hidden = true; api('/api/launch', { package: a.package }).catch(() => toast(t('actionErr'), true)); };
    grid.append(b);
  }
};

let wake = null;
async function keepAwake() {
  try { if ('wakeLock' in navigator && !document.hidden) wake = await navigator.wakeLock.request('screen'); } catch {}
}
document.addEventListener('visibilitychange', () => {
  // Stop streaming in the background so the computer and iPad can rest.
  if (document.hidden) { disconnect(); if (wake) { wake.release().catch(() => {}); wake = null; } }
  else if (!$('#player').hidden) { setOverlay('linking'); connect(); keepAwake(); }
});
addEventListener('resize', () => requestAnimationFrame(fit));
addEventListener('orientationchange', () => setTimeout(fit, 250));

function enterPlayer() { show('player'); setOverlay('linking'); fit(); connect(); keepAwake(); }

async function route() {
  let paired = false;
  try { paired = (await api('/api/me')).paired; } catch {}
  if (paired) return enterPlayer();
  // Home Screen apps keep their own storage, so pair after installing.
  if (navigator.standalone === false && !sessionStorage.getItem('guy.skip')) { show('install'); return; }
  show('pair');
}
$('#skipInstall').onclick = () => { try { sessionStorage.setItem('guy.skip', '1'); } catch {} show('pair'); };

buildPad(); drawDots(); applyText(); route();
})();
