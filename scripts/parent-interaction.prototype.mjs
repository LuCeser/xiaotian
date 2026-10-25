// PROTOTYPE ONLY. Serve the existing parent.html shell with memory-only UI variants.
// Run: node scripts/parent-interaction.prototype.mjs
import http from 'node:http';
import { readFileSync, mkdirSync, openSync, closeSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { spawn } from 'node:child_process';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const staticRoot = path.join(root, 'pkg/web/static');
const port = 4178;
const url = `http://127.0.0.1:${port}/parent.html?prototype=interaction&variant=A`;
if (!process.argv.includes('--serve')) {
  const ready = async () => {
    try { return (await fetch(`http://127.0.0.1:${port}/__prototype_health`, { signal: AbortSignal.timeout(600) })).ok; }
    catch { return false; }
  };
  if (!await ready()) {
    const logDir = path.join(root, 'logs');
    mkdirSync(logDir, { recursive: true });
    const log = openSync(path.join(logDir, 'parent-interaction.prototype.log'), 'a');
    const child = spawn(process.execPath, [fileURLToPath(import.meta.url), '--serve'], {
      cwd: root, detached: true, windowsHide: true, stdio: ['ignore', log, log],
    });
    writeFileSync(path.join(logDir, 'parent-interaction.prototype.pid'), String(child.pid));
    child.unref(); closeSync(log);
    for (let i = 0; i < 30 && !await ready(); i++) await new Promise(r => setTimeout(r, 150));
    if (!await ready()) throw new Error('Prototype did not start. See logs/parent-interaction.prototype.log');
  }
  console.log(`Prototype ready: ${url}`);
} else {
  http.createServer((req, res) => {
    const request = new URL(req.url, `http://127.0.0.1:${port}`);
    const pathname = decodeURIComponent(request.pathname);
    if (pathname === '/__prototype_health') {
      res.writeHead(200, { 'Content-Type': 'text/plain' }); res.end('parent-interaction-prototype'); return;
    }
    if (pathname === '/favicon.ico') { res.writeHead(204); res.end(); return; }
    // There is deliberately no proxy or API backend on this server.
    if (req.method !== 'GET' || pathname.startsWith('/api/')) { res.writeHead(405); res.end(); return; }
    if (pathname === '/' || pathname === '/parent.html') {
      let html = readFileSync(path.join(staticRoot, 'parent.html'), 'utf8');
      html = html.replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, '');
      html = html.replace('<html lang="zh-CN">', '<html lang="zh-CN" data-interaction-prototype="true">');
      html = html.replace('</head>', '<link rel="stylesheet" href="/styles/parent-interaction.prototype.css"></head>');
      html = html.replace('</body>', '<script src="/js/parent-interaction.prototype.js" defer></script></body>');
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
      res.end(html); return;
    }
    const file = path.resolve(staticRoot, '.' + pathname);
    if (!file.startsWith(staticRoot + path.sep)) { res.writeHead(403); res.end(); return; }
    try {
      const type = { '.css': 'text/css', '.js': 'text/javascript', '.svg': 'image/svg+xml', '.png': 'image/png' }[path.extname(file)] || 'application/octet-stream';
      const body = readFileSync(file);
      res.writeHead(200, { 'Content-Type': type + '; charset=utf-8', 'Cache-Control': 'no-store' });
      res.end(body);
    } catch { res.writeHead(404); res.end(); }
  }).listen(port, '127.0.0.1', () => console.log(`Prototype server listening on ${port}`));
}
