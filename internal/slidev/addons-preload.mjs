// Stagepack's addons preload: node --import <this> <rt>/@slidev/cli/bin/slidev.mjs ./slides.md ...
//
// It makes Slidev load extra addons, such as an editor's, without writing anything into the
// talk. Slidev 53 has no option for that. It resolves addons from the headmatter and from the
// talk's package.json `slidev.addons`, which getRoots reads once and caches. This preload calls
// getRoots first, with the entry Slidev will compute, and swaps the cached package.json for a
// copy with the extra addons appended. The file on disk is never written.
//
// STAGEPACK_EXTRA_ADDONS is a JSON array of absolute addon folders (forward slashes, also on
// Windows). Unset or [], the preload does nothing.
import { readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

const raw = process.env.STAGEPACK_EXTRA_ADDONS
const extra = raw ? JSON.parse(raw) : []
if (!Array.isArray(extra) || extra.some((p) => typeof p !== 'string'))
  throw new Error('stagepack: STAGEPACK_EXTRA_ADDONS is not a JSON array of paths')

if (extra.length > 0) {
  const entry = process.argv[2]
  if (!entry || entry.startsWith('-'))
    throw new Error('stagepack: the addons preload needs the slides entry right after the CLI')
  const roots = await (await findGetRoots())(slidevEntry(entry))
  const pkg = roots.userPkgJson ?? {}
  const slidev = isObject(pkg.slidev) ? pkg.slidev : {}
  const own = Array.isArray(slidev.addons) ? slidev.addons : []
  roots.userPkgJson = { ...pkg, slidev: { ...slidev, addons: [...own, ...extra] } }
}

// findGetRoots imports the resolver chunks of the runtime the CLI path (argv[1]) points at.
// Their exports are minified, so getRoots is found by its function name.
async function findGetRoots() {
  const dist = join(dirname(process.argv[1]), '..', 'dist')
  let names = []
  try {
    names = readdirSync(dist).filter((n) => n.startsWith('resolver-') && n.endsWith('.mjs')).sort()
  }
  catch {}
  for (const name of names) {
    const mod = await import(pathToFileURL(join(dist, name)).href)
    const fn = Object.values(mod).find((v) => typeof v === 'function' && v.name === 'getRoots')
    if (fn)
      return fn
  }
  throw new Error("stagepack: this Slidev runtime has no getRoots; extra addons can't be loaded")
}

// slidevEntry is what Slidev's resolveEntry makes of the entry, slash(pathe.resolve(p)): pathe
// turns backslashes into slashes and upper-cases the drive letter of its argument, but not of
// the working folder, and slash turns the working folder's backslashes into slashes.
function slidevEntry(p) {
  return resolve(p.replace(/\\/g, '/').replace(/^[a-z]:\//, (d) => d.toUpperCase())).replace(/\\/g, '/')
}

function isObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v)
}
