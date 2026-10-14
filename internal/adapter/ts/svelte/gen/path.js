// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// A POSIX path, enough for svelte2tsx's file names.
function normalize(p) {
  const abs = p.startsWith('/'), out = [];
  for (const s of p.split('/')) {
    if (s === '' || s === '.') continue;
    if (s === '..') { if (out.length && out[out.length - 1] !== '..') out.pop(); else if (!abs) out.push('..'); continue; }
    out.push(s);
  }
  return (abs ? '/' : '') + out.join('/') || (abs ? '/' : '.');
}
function join(...ps) { return normalize(ps.filter(Boolean).join('/')); }
function resolve(...ps) { let r = ''; for (const p of ps) r = p.startsWith('/') ? p : r + '/' + p; return normalize(r || '/'); }
function dirname(p) { const i = p.lastIndexOf('/'); return i <= 0 ? (i === 0 ? '/' : '.') : p.slice(0, i); }
function basename(p, ext) { let b = p.slice(p.lastIndexOf('/') + 1); if (ext && b.endsWith(ext)) b = b.slice(0, -ext.length); return b; }
function extname(p) { const b = basename(p), i = b.lastIndexOf('.'); return i <= 0 ? '' : b.slice(i); }
function relative(from, to) {
  const f = resolve(from).split('/').filter(Boolean), t = resolve(to).split('/').filter(Boolean);
  let i = 0; while (i < f.length && i < t.length && f[i] === t[i]) i++;
  return [...Array(f.length - i).fill('..'), ...t.slice(i)].join('/');
}
function isAbsolute(p) { return p.startsWith('/'); }
function parse(p) { const base = basename(p), ext = extname(p); return { root: p.startsWith('/') ? '/' : '', dir: dirname(p), base, ext, name: ext ? base.slice(0, -ext.length) : base }; }
function format(o) { return (o.dir ? o.dir + '/' : '') + (o.base || (o.name || '') + (o.ext || '')); }
const sep = '/', delimiter = ':';
const posix = { normalize, join, resolve, dirname, basename, extname, relative, isAbsolute, parse, format, sep, delimiter };
posix.posix = posix; posix.win32 = posix;
module.exports = posix;
