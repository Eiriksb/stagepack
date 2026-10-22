#!/usr/bin/env node
// The npm launcher for Stagepack. The viewer, its Node and the Slidev runtime (about 800 MB) are not in
// this package: `stagepack install` downloads the build for this platform from the GitHub release of the
// same version, checks it against the SHA256SUMS shipped in this package, and runs its own installer
// (`stagepack install` from the Linux tarball, or Stagepack-Setup.exe on Windows). Every other command runs
// the installed viewer (~/.local/share/stagepack/stagepack, %LOCALAPPDATA%\Stagepack\stagepack.exe).
//
// STAGEPACK_DOWNLOAD_URL: a folder holding the release files, instead of the GitHub release.
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { Readable, Transform } = require('node:stream');
const { pipeline } = require('node:stream/promises');

const VERSION = require('../package.json').version;
const REPO = 'Eiriksb/stagepack';
const win = process.platform === 'win32';

function say(msg) {
  process.stderr.write(`stagepack: ${msg}\n`);
}

// fail ends the launcher with msg (and no stack trace), after any cleanup on the way out.
class Failure extends Error {}

function fail(msg) {
  throw new Failure(msg);
}

// The install folder `stagepack install` uses by default, and the viewer in it.
function installedExe() {
  if (win) {
    if (!process.env.LOCALAPPDATA) return null;
    return path.join(process.env.LOCALAPPDATA, 'Stagepack', 'stagepack.exe');
  }
  const xdg = process.env.XDG_DATA_HOME;
  const data = xdg && path.isAbsolute(xdg) ? xdg : path.join(os.homedir(), '.local', 'share');
  return path.join(data, 'stagepack', 'stagepack');
}

// The installed viewer's version: null when there is none, '' when it is too old to say.
function installedVersion(exe) {
  if (!exe || !fs.existsSync(exe)) return null;
  const r = spawnSync(exe, ['version'], { encoding: 'utf8', windowsHide: true });
  return r.status === 0 ? r.stdout.trim() : '';
}

function run(exe, args) {
  const r = spawnSync(exe, args, { stdio: 'inherit' });
  if (r.error) fail(`cannot run ${exe}: ${r.error.message}`);
  if (r.signal) process.kill(process.pid, r.signal);
  return r.status ?? 1;
}

function downloadBase() {
  const env = process.env.STAGEPACK_DOWNLOAD_URL;
  if (env) return env.replace(/\/+$/, '');
  if (VERSION.endsWith('-dev')) fail('this is a development copy of the launcher: set STAGEPACK_DOWNLOAD_URL');
  return `https://github.com/${REPO}/releases/download/v${VERSION}`;
}

// The release file for this platform and its checksum, from the SHA256SUMS in this package.
function asset() {
  if (process.arch !== 'x64' || !['linux', 'win32'].includes(process.platform)) {
    fail(`there is no Stagepack build for ${process.platform}-${process.arch}; there are for linux-x64 and win32-x64`);
  }
  let sums;
  try {
    sums = fs.readFileSync(path.join(__dirname, '..', 'SHA256SUMS'), 'utf8');
  } catch {
    fail('this package has no SHA256SUMS to check the download against (a development copy?)');
  }
  const want = win ? /^Stagepack-Setup\.exe$/ : /^stagepack-[0-9A-Za-z.-]+-linux-x64\.tar\.xz$/;
  for (const line of sums.split('\n')) {
    const m = /^([0-9a-f]{64}) {2}(\S+)$/.exec(line.trim());
    if (m && want.test(m[2])) return { name: m[2], sha256: m[1] };
  }
  fail('SHA256SUMS lists no build for this platform');
}

async function download(url, file, sha256) {
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok || !res.body) throw new Error(`${url}: HTTP ${res.status}`);
  const total = Number(res.headers.get('content-length')) || 0;
  const hash = crypto.createHash('sha256');
  let got = 0;
  let shown = -1;
  const tty = process.stderr.isTTY;
  const count = new Transform({
    transform(chunk, _enc, cb) {
      hash.update(chunk);
      got += chunk.length;
      const pct = total ? Math.floor((got * 100) / total) : -1;
      if (tty && pct !== shown) {
        shown = pct;
        process.stderr.write(`\r  ${(got / 1e6).toFixed(0)} MB${total ? ` of ${(total / 1e6).toFixed(0)} MB (${pct}%)` : ''}`);
      }
      cb(null, chunk);
    },
  });
  await pipeline(Readable.fromWeb(res.body), count, fs.createWriteStream(file));
  if (tty) process.stderr.write('\n');
  const digest = hash.digest('hex');
  if (digest !== sha256) throw new Error(`${path.basename(file)} does not match its checksum (got ${digest}, want ${sha256})`);
}

// A temporary folder on disk: not /tmp on Linux, which is often a small tmpfs (the build unpacks to 800 MB).
function tempDir() {
  const parent = win ? os.tmpdir() : process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache');
  fs.mkdirSync(parent, { recursive: true });
  return fs.mkdtempSync(path.join(parent, 'stagepack-install-'));
}

// install downloads the release for this platform and runs its installer with args.
async function install(args) {
  const a = asset();
  const url = `${downloadBase()}/${a.name}`;
  const tmp = tempDir();
  try {
    say(`downloading ${url}`);
    const file = path.join(tmp, a.name);
    try {
      await download(url, file, a.sha256);
    } catch (e) {
      fail(`${e.message}. Nothing was installed.`);
    }
    say('checksum OK');
    if (win) return run(file, ['install', ...args]);
    const x = path.join(tmp, 'x');
    fs.mkdirSync(x);
    const t = spawnSync('tar', ['-xJf', file, '-C', x, '--no-same-owner'], { stdio: 'inherit' });
    if (t.status !== 0) fail('could not unpack it with tar (is xz installed?)');
    return run(path.join(x, a.name.replace(/\.tar\.xz$/, ''), 'stagepack'), ['install', ...args]);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

async function main() {
  const args = process.argv.slice(2);
  const exe = installedExe();
  const have = installedVersion(exe);
  const cmd = args[0];

  if (cmd === 'install') {
    // The same version installed already: its own install only registers the file type again.
    if (have === VERSION && !args.includes('--home') && !args.some((x) => x.startsWith('--home='))) {
      return run(exe, args);
    }
    return install(args.slice(1));
  }
  if (cmd === 'version' || cmd === '--version') {
    console.log(`${VERSION} (npm launcher); installed: ${have === null ? 'none' : have || 'unknown'}`);
    return 0;
  }
  if (have === null) {
    say(`Stagepack is not installed yet. Run: stagepack install (downloads about ${win ? 190 : 120} MB)`);
    return 1;
  }
  if (have !== VERSION && cmd !== 'uninstall') {
    say(`the installed viewer is ${have || 'an older version'}, this package is ${VERSION}: run stagepack install to update it`);
  }
  return run(exe, args);
}

main().then(
  (code) => process.exit(code),
  (e) => {
    say(e instanceof Failure ? e.message : e.stack || String(e));
    process.exit(1);
  },
);
