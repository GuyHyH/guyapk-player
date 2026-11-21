// Tests GuyAPK Remote in Safari on a real iPadOS Simulator (macOS only).
// It serves a recorded Android stream over the real TLS setup.
import { execFileSync, spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..');
const work = path.join(process.env.RUNNER_TEMP || '/tmp', 'guyapk-ipad');
const shots = path.join(root, 'ipad-test-results');
fs.rmSync(work, { recursive: true, force: true });
fs.mkdirSync(work, { recursive: true });
fs.mkdirSync(shots, { recursive: true });

const sleep = ms => new Promise(r => setTimeout(r, ms));
const sh = (cmd, ...args) => execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
const results = [];
const check = (name, ok, extra = '') => {
  results.push({ name, ok: !!ok, extra: String(extra) });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra !== '' ? '  [' + extra + ']' : ''}`);
};

function lanIP() {
  for (const n of ['en0', 'en1', 'en2', 'en3', 'en4', 'en5', 'en6']) {
    try {
      const ip = sh('ipconfig', 'getifaddr', n);
      if (/^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(ip)) return ip;
    } catch {}
  }
  throw new Error('no private LAN address');
}

function pickIPad() {
  const data = JSON.parse(sh('xcrun', 'simctl', 'list', 'devices', 'available', '-j')).devices;
  const ver = k => (k.match(/iOS-(\d+)-(\d+)/) || []).slice(1).map(Number);
  const runtimes = Object.keys(data).filter(k => k.includes('iOS'))
    .sort((a, b) => { const x = ver(a), y = ver(b); return (y[0] - x[0]) || (y[1] - x[1]); });
  for (const rt of runtimes) {
    const list = data[rt].filter(d => /^iPad/.test(d.name));
    const dev = list.find(d => /iPad Pro/.test(d.name)) || list[0];
    if (dev) return { udid: dev.udid, name: dev.name, runtime: rt.split('.').pop() };
  }
  throw new Error('no iPad simulator');
}

async function waitFor(fn, ms, label) {
  const end = Date.now() + ms;
  let last;
  while (Date.now() < end) {
    try { last = await fn(); if (last) return last; } catch (e) { last = undefined; }
    await sleep(250);
  }
  throw new Error('timed out: ' + label);
}

const ip = lanIP();
console.log('LAN address', ip);
const harness = spawn('go', ['test', './internal/remote', '-run', '^TestHarness$', '-count=1', '-timeout', '25m'], {
  cwd: root, stdio: 'inherit',
  env: { ...process.env, GUYAPK_HARNESS: 'replay', GUYAPK_HARNESS_DIR: work, GUYAPK_HARNESS_IP: ip },
});
const hfile = path.join(work, 'harness.json');
const readHarness = () => JSON.parse(fs.readFileSync(hfile, 'utf8'));

let driver, session;
const wd = async (method, url, body) => {
  const res = await fetch(`http://127.0.0.1:4723/session${session ? '/' + session : ''}${url}`, {
    method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(JSON.stringify(data.value).slice(0, 300));
  return data.value;
};
const js = (script, ...args) => wd('POST', '/execute/sync', { script, args });
const shot = async name => {
  const b = await wd('GET', '/screenshot');
  fs.writeFileSync(path.join(shots, name), Buffer.from(b, 'base64'));
};

let device;
try {
  await waitFor(() => fs.existsSync(hfile), 120000, 'test server');
  const h = readHarness();
  console.log('setup', h.setup, 'app', h.app);

  const profile = await (await fetch(h.setup + 'ca.mobileconfig')).arrayBuffer();
  const pfile = path.join(work, 'GuyAPK.mobileconfig');
  fs.writeFileSync(pfile, Buffer.from(profile));
  let lint = '';
  try { lint = sh('plutil', '-lint', pfile); } catch (e) { lint = String(e.stdout || e.message); }
  check('configuration profile is a valid plist', lint.endsWith('OK'), lint.split(' ').pop());
  fs.copyFileSync(pfile, path.join(shots, 'GuyAPK.mobileconfig'));

  device = pickIPad();
  console.log('Simulator', device.name, device.runtime);
  try { sh('xcrun', 'simctl', 'boot', device.udid); } catch {}
  sh('xcrun', 'simctl', 'bootstatus', device.udid, '-b');
  sh('xcrun', 'simctl', 'keychain', device.udid, 'add-root-cert', h.ca);

  sh('sudo', 'safaridriver', '--enable');
  driver = spawn('safaridriver', ['-p', '4723'], { stdio: 'inherit' });
  await waitFor(async () => (await fetch('http://127.0.0.1:4723/status')).ok, 20000, 'safaridriver');
  const created = await waitFor(async () => {
    session = undefined;
    return wd('POST', '', { capabilities: { alwaysMatch: {
      browserName: 'safari', platformName: 'iOS', 'safari:useSimulator': true, 'safari:deviceUDID': device.udid,
    } } });
  }, 240000, 'Safari session');
  session = created.sessionId;
  check('Safari runs on iPadOS Simulator', created.capabilities?.platformName?.toLowerCase() === 'ios', `${device.name} ${device.runtime}`);

  await wd('POST', '/url', { url: h.setup });
  const trusted = await waitFor(() => js(`return document.querySelector('#status')?.classList.contains('ok')`), 20000, 'certificate trust').catch(() => false);
  check('iPadOS trusts the GuyAPK certificate', trusted);
  await shot('1-setup.png');

  await wd('POST', '/url', { url: h.app });
  await waitFor(() => js(`return !!document.querySelector('.pad button')`), 15000, 'app page');
  const env = await js(`return {secure:isSecureContext, decoder:typeof VideoDecoder, standalone:navigator.standalone, ua:navigator.userAgent}`);
  check('page is a secure context', env.secure);
  check('Safari provides VideoDecoder', env.decoder === 'function', env.decoder);
  const links = await js(`return {cap:document.querySelector('meta[name=apple-mobile-web-app-capable]')?.content, icon:document.querySelector('link[rel=apple-touch-icon]')?.href, manifest:document.querySelector('link[rel=manifest]')?.href}`);
  const iconRes = await fetch(h.setup + 'icon-180.png');
  check('Home Screen metadata is present', links.cap === 'yes' && !!links.icon && !!links.manifest && iconRes.ok);
  const install = await waitFor(() => js(`return !document.querySelector('#install').hidden`), 5000, 'install guide').catch(() => false);
  check('shows Add to Home Screen guide in Safari', install);
  await shot('2-install-guide.png');
  await js(`document.querySelector('#skipInstall').click()`);

  await js(`for (const d of arguments[0]) [...document.querySelectorAll('#pad button')].find(b => b.textContent === d).click();`, '000000' === h.pin ? '000001' : '000000');
  const wrong = await waitFor(() => js(`return !document.querySelector('#pairErr').hidden`), 6000, 'wrong PIN message').catch(() => false);
  check('wrong PIN is rejected', wrong);
  await shot('3-pin.png');
  await js(`for (const d of arguments[0]) [...document.querySelectorAll('#pad button')].find(b => b.textContent === d).click();`, h.pin);
  const entered = await waitFor(() => js(`return !document.querySelector('#player').hidden`), 8000, 'player').catch(() => false);
  check('correct PIN pairs', entered);

  const playing = await waitFor(() => js(`const s=GuyRemote.stats; return s.frames>=20 && document.querySelector('#overlay').hidden ? s : null`), 45000, 'video frames').catch(() => null);
  const stats = await js(`return GuyRemote.stats`);
  check('H.264 video decodes and displays', !!playing, `frames ${stats.frames}, ${stats.width}x${stats.height}, errors ${stats.decoderErrors}`);
  const f1 = stats.frames; await sleep(2500);
  const moving = await js(`return GuyRemote.stats`);
  check('video keeps playing', moving.frames > f1 && moving.decoderErrors === 0, `${moving.frames - f1} frames in 2.5s`);
  const pixels = await js(`const c=document.querySelector('#screen'),x=c.getContext('2d').getImageData(0,0,c.width,c.height).data; let n=0; for(let i=0;i<x.length;i+=4000) if(x[i]+x[i+1]+x[i+2]>30) n++; return n`);
  check('canvas shows a real picture', pixels > 20, `${pixels} lit samples`);
  await sleep(300); await shot('4-playing.png');

  const before = readHarness().touches || 0;
  await js(`const c=document.querySelector('#screen'),r=c.getBoundingClientRect(),o={bubbles:true,cancelable:true,pointerId:5,pointerType:'touch',isPrimary:true,clientX:r.left+r.width/2,clientY:r.top+r.height/2};
    c.dispatchEvent(new PointerEvent('pointerdown',o)); c.dispatchEvent(new PointerEvent('pointermove',{...o,clientY:o.clientY+40})); c.dispatchEvent(new PointerEvent('pointerup',{...o,clientY:o.clientY+40}));`);
  const got = await waitFor(() => { const n = readHarness().touches || 0; return n >= before + 3 ? n : 0; }, 5000, 'touches').catch(() => 0);
  check('touch reaches the computer', got >= before + 3, `${got - before} events`);

  await js(`document.querySelector('#apps').click()`);
  const apps = await waitFor(() => js(`const a=[...document.querySelectorAll('#grid button span')].map(x=>x.textContent); return a.length?a:null`), 5000, 'apps').catch(() => []);
  check('app list loads', apps.includes('นิยายออนไลน์'), apps.join(', '));
  await sleep(250); await shot('5-apps.png');
  await js(`document.querySelector('#closeDrawer').click()`);
  const fits = await js(`const c=document.querySelector('#screen').getBoundingClientRect(); return c.width>200 && c.height>200 && c.right<=innerWidth+1 && c.bottom<=innerHeight+1 && document.documentElement.scrollWidth<=innerWidth`);
  check('picture fits the iPad screen', fits);
} catch (e) {
  check('test run', false, e.message);
} finally {
  if (session) { try { await wd('DELETE', ''); } catch {} }
  if (driver) driver.kill();
  try { if (device) sh('xcrun', 'simctl', 'io', device.udid, 'screenshot', path.join(shots, 'device.png')); } catch {}
  fs.writeFileSync(path.join(work, 'stop'), '');
  await Promise.race([new Promise(r => harness.on('exit', r)), sleep(10000)]);
  harness.kill();
}
fs.writeFileSync(path.join(shots, 'results.json'), JSON.stringify({ device, results }, null, 2));
const failed = results.filter(r => !r.ok).length;
console.log(`\n${results.length - failed}/${results.length} passed`);
process.exit(failed ? 1 : 0);
