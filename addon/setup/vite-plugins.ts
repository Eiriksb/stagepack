// The dev-server half of the addon. It adds two endpoints to `slidev` (dev mode only), for this computer
// only (`slidev --remote` serves the LAN too, and the export holds the whole talk folder):
//   GET  /__stagepack/status   {"stagepack": <path of the stagepack program, or null>}
//   POST /__stagepack/export   runs `stagepack pack` on this talk and answers with the .sptx file
// The packing is Stagepack's own `stagepack pack` (the installed viewer, $STAGEPACK_BIN, or `stagepack` on
// PATH), so a talk exported here is the same as one packed on the command line.
//
// In Stagepack's viewer, which bundles this addon so that talks naming it open there, it does nothing:
// the viewer sets STAGEPACK_VIEWER, and the button hides itself when the endpoints are missing.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { spawn } from 'node:child_process'
import { createReadStream, existsSync, mkdtempSync, rmSync, statSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { basename, delimiter, isAbsolute, join, relative, resolve } from 'node:path'

interface SlidevOptions {
  mode: string
  userRoot: string
  entry: string
}

const PREFIX = '/__stagepack/'

export default function setup(options: SlidevOptions): Plugin[] {
  if (options.mode !== 'dev' || process.env.STAGEPACK_VIEWER)
    return []
  let busy = false
  return [{
    name: 'stagepack:export',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = (req.url || '').split('?')[0]
        const at = path.lastIndexOf(PREFIX) // after any base path
        if (at < 0)
          return next()
        if (!fromThisComputer(req))
          return next()
        const what = path.slice(at + PREFIX.length)
        if (what === 'status' && req.method === 'GET')
          return json(res, 200, { stagepack: findStagepack() })
        if (what === 'export' && req.method === 'POST') {
          if (!sameOrigin(req))
            return json(res, 403, { error: 'only this Slidev page can export the talk' })
          if (busy)
            return json(res, 409, { error: 'an export is already running' })
          busy = true
          exportTalk(options, res).catch(() => res.destroy()).finally(() => { busy = false })
          return
        }
        return json(res, 404, { error: 'not found' })
      })
    },
  }]
}

// findStagepack is the stagepack program to pack with: $STAGEPACK_BIN, the per-user install
// (`stagepack install`), or stagepack on PATH. Null when there is none.
function findStagepack(): string | null {
  const env = process.env.STAGEPACK_BIN
  if (env) {
    const p = resolve(env)
    return existsSync(p) ? p : null
  }
  const win = process.platform === 'win32'
  let installed: string | undefined
  if (win) {
    if (process.env.LOCALAPPDATA)
      installed = join(process.env.LOCALAPPDATA, 'Stagepack', 'stagepack.exe')
  }
  else {
    const xdg = process.env.XDG_DATA_HOME
    installed = join(xdg && isAbsolute(xdg) ? xdg : join(homedir(), '.local', 'share'), 'stagepack', 'stagepack')
  }
  if (installed && existsSync(installed))
    return installed
  for (const dir of (process.env.PATH || '').split(delimiter)) {
    if (!dir || !isAbsolute(dir)) // not ".": that would run a stagepack from the talk's folder
      continue
    const p = join(dir, win ? 'stagepack.exe' : 'stagepack')
    if (existsSync(p) && statSync(p).isFile())
      return p
  }
  return null
}

// fromThisComputer: the request came over loopback.
function fromThisComputer(req: IncomingMessage): boolean {
  const a = req.socket.remoteAddress || ''
  return a === '::1' || a.startsWith('127.') || a.startsWith('::ffff:127.')
}

// sameOrigin keeps other web pages from making the browser export the talk: a POST from another
// site carries its own Origin (and Sec-Fetch-Site: cross-site). One with neither is refused too.
function sameOrigin(req: IncomingMessage): boolean {
  const site = req.headers['sec-fetch-site']
  if (site && site !== 'same-origin')
    return false
  const origin = req.headers.origin
  if (!origin)
    return site === 'same-origin'
  try {
    return new URL(origin).host === req.headers.host
  }
  catch {
    return false
  }
}

async function exportTalk(options: SlidevOptions, res: ServerResponse): Promise<void> {
  const bin = findStagepack()
  if (!bin) {
    json(res, 500, { error: 'Stagepack is not installed. Install it (npx stagepack install, or see https://github.com/Eiriksb/stagepack#install), or set STAGEPACK_BIN.' })
    return
  }
  let dir = ''
  try {
    dir = mkdtempSync(join(tmpdir(), 'stagepack-export-'))
    const out = join(dir, 'talk.sptx')
    const args = ['pack', options.userRoot, '-o', out, '--entry', relative(options.userRoot, options.entry)]
    const { code, stdout, stderr } = await run(bin, args, options.userRoot)
    if (code !== 0 || !existsSync(out)) {
      json(res, 500, { error: (stderr || stdout).trim() || `stagepack pack failed (exit ${code})` })
      return
    }
    const name = `${safeName(packedTitle(stdout) || basename(options.userRoot))}.sptx`
    res.statusCode = 200
    res.setHeader('Content-Type', 'application/x-stagepack')
    res.setHeader('Content-Length', String(statSync(out).size))
    res.setHeader('Content-Disposition', `attachment; filename="${name.replace(/[^\x20-\x7E]|["\\]/g, '_')}"; filename*=UTF-8''${encodeURIComponent(name)}`)
    // pack's notes (fonts that need the internet, code the viewer will ask about), for the button to show
    res.setHeader('X-Stagepack-Notes', encodeURIComponent(stderr.trim().slice(0, 4000)))
    res.setHeader('Cache-Control', 'no-store')
    await new Promise<void>((resolve) => {
      const s = createReadStream(out)
      s.on('error', () => { res.destroy(); resolve() })
      res.on('close', () => { s.destroy(); resolve() })
      s.pipe(res)
    })
  }
  catch (e) {
    if (!res.headersSent)
      json(res, 500, { error: String((e as Error)?.message || e) })
    else
      res.destroy()
  }
  finally {
    try {
      if (dir) // Windows may hold the file a moment after a cancelled download
        rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 })
    }
    catch {}
  }
}

function run(bin: string, args: string[], cwd: string): Promise<{ code: number | null, stdout: string, stderr: string }> {
  return new Promise((resolve, reject) => {
    const p = spawn(bin, args, { cwd, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true })
    let stdout = ''
    let stderr = ''
    p.stdout.on('data', (d) => { stdout += d })
    p.stderr.on('data', (d) => { stderr += d })
    p.on('error', reject)
    p.on('close', code => resolve({ code, stdout, stderr }))
  })
}

// packedTitle is the title from pack's `packed "Title" (Slidev …) into …` line.
function packedTitle(stdout: string): string {
  const m = /^packed (".*") \(Slidev /m.exec(stdout)
  if (!m)
    return ''
  try {
    return String(JSON.parse(m[1]))
  }
  catch {
    return ''
  }
}

// safeName makes a title a file name on every OS.
function safeName(title: string): string {
  const s = title.replace(/[<>:"/\\|?*\x00-\x1F]+/g, ' ').replace(/\s+/g, ' ').trim().replace(/[. ]+$/, '').slice(0, 100)
  return s || 'talk'
}

function json(res: ServerResponse, status: number, body: unknown): void {
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json')
  res.setHeader('Cache-Control', 'no-store')
  res.end(JSON.stringify(body))
}
