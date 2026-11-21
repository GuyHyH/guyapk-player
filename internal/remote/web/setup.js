(() => {
'use strict';
const T = {
  th: {
    title: 'ตั้งค่า iPad', sub: 'ทำครั้งเดียว เพื่อให้การเชื่อมต่อกับคอมพิวเตอร์เข้ารหัสอย่างปลอดภัย',
    s1: 'ดาวน์โหลดใบรับรอง', s1d: 'กดปุ่มด้านล่าง แล้วเลือก "อนุญาต"', download: 'ดาวน์โหลดใบรับรอง',
    s2: 'ติดตั้งใบรับรอง', s2d: 'ไปที่ การตั้งค่า > ดาวน์โหลดโปรไฟล์แล้ว (หรือ ทั่วไป > VPN และการจัดการอุปกรณ์) เลือก GuyAPK Remote แล้วกด "ติดตั้ง"',
    s3: 'เปิดความเชื่อถือ', s3d: 'ไปที่ การตั้งค่า > ทั่วไป > เกี่ยวกับ > การตั้งค่าความเชื่อถือใบรับรอง แล้วเปิดสวิตช์ GuyAPK Remote Local CA',
    waiting: 'กำลังรอให้ iPad เชื่อถือใบรับรอง…', ready: 'เชื่อมต่อปลอดภัยพร้อมแล้ว', open: 'เปิด GuyAPK Remote',
    note: 'ทำขั้นตอนนี้บน Wi-Fi ที่คุณเชื่อถือเท่านั้น ใบรับรองนี้ใช้ได้เฉพาะที่อยู่ในเครือข่ายภายใน และลบได้ทุกเมื่อจากการตั้งค่า', lang: 'English',
  },
  en: {
    title: 'Set up iPad', sub: 'A one-time step that encrypts the connection to your computer',
    s1: 'Download the certificate', s1d: 'Tap the button below, then choose "Allow"', download: 'Download certificate',
    s2: 'Install the certificate', s2d: 'Open Settings > Profile Downloaded (or General > VPN & Device Management), choose GuyAPK Remote, then tap Install',
    s3: 'Turn on trust', s3d: 'Open Settings > General > About > Certificate Trust Settings and turn on GuyAPK Remote Local CA',
    waiting: 'Waiting for this iPad to trust the certificate…', ready: 'Secure connection is ready', open: 'Open GuyAPK Remote',
    note: 'Only do this on a Wi-Fi network you trust. The certificate works only for private network addresses and can be removed in Settings at any time.', lang: 'ไทย',
  },
};
let lang = 'th', trusted = false, app = '';
try { lang = localStorage.getItem('guy.lang') === 'en' ? 'en' : 'th'; } catch {}
const $ = s => document.querySelector(s);

function render() {
  document.documentElement.lang = lang;
  document.querySelectorAll('[data-t]').forEach(n => { n.textContent = T[lang][n.dataset.t]; });
  $('#lang').textContent = T[lang].lang;
  $('#status').textContent = T[lang][trusted ? 'ready' : 'waiting'];
  $('#status').classList.toggle('ok', trusted);
  $('#open').hidden = !trusted;
  $('#steps').hidden = trusted;
}
$('#lang').onclick = () => { lang = lang === 'th' ? 'en' : 'th'; try { localStorage.setItem('guy.lang', lang); } catch {} render(); };

async function check() {
  try {
    if (!app) { app = (await (await fetch('/setup.json')).json()).app; $('#open').href = app; }
    // This request only succeeds once the iPad trusts the certificate.
    await fetch(app + 'api/ping', { mode: 'no-cors', cache: 'no-store' });
    if (!trusted) { trusted = true; render(); }
  } catch { if (trusted) { trusted = false; render(); } }
}
render(); check(); setInterval(check, 2000);
})();
