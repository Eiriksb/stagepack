// Usage: node build/licenses.mjs <runtime dir>
// The licences of everything the Stagepack runtime ships: every package reachable from <dir>/package.json's
// dependencies (plus the optional ones npm installed for this os/cpu, which ship too). Writes them to
// <dir>/THIRD_PARTY_LICENSES.txt and fails, listing them, when one is not on the list below.
// Policy: Stagepack ships only permissively licensed components (plus the listed weak-copyleft EXCEPTIONS
// below, unmodified: warned about, listed apart). Compound expressions follow the "A OR B" / "A AND B" rule.
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

// Permissive licences only. PSF-2.0, Python-2.0, Zlib and CC-BY-4.0 are there for what the runtime needs
// (argparse: PSF/Python-2.0, pako: Zlib, caniuse-lite's data: CC-BY-4.0).
const ALLOWED = new Set(["MIT", "ISC", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "0BSD", "OFL-1.1", "Unlicense", "CC0-1.0", "BlueOak-1.0.0",
  "PSF-2.0", "Python-2.0", "Zlib", "CC-BY-4.0"]);

if (process.argv.length !== 3) {
  console.error("usage: node build/licenses.mjs <runtime dir>");
  process.exit(2);
}
const top = resolve(process.argv[2]);

const seen = new Map();
function visit(name, from) {
  // node's resolution: the nearest node_modules up from the package that depends on it
  let dir = from, pkgDir = null;
  for (;;) {
    const c = join(dir, "node_modules", name);
    if (existsSync(join(c, "package.json"))) { pkgDir = c; break; }
    const up = dirname(dir);
    if (up === dir) break;
    dir = up;
  }
  if (!pkgDir || seen.has(pkgDir)) return; // an optional dependency not installed for this os/cpu
  const pkg = JSON.parse(readFileSync(join(pkgDir, "package.json"), "utf8"));
  seen.set(pkgDir, pkg);
  for (const dep of Object.keys(pkg.dependencies || {})) visit(dep, pkgDir);
  for (const dep of Object.keys(pkg.optionalDependencies || {})) visit(dep, pkgDir);
  for (const dep of Object.keys(pkg.peerDependencies || {})) if (!pkg.peerDependenciesMeta?.[dep]?.optional) visit(dep, pkgDir);
}
const root = JSON.parse(readFileSync(join(top, "package.json"), "utf8"));
for (const dep of Object.keys(root.dependencies || {})) visit(dep, top);

// Packages with no "license" field whose licence file was read and checked by hand. Keyed by exact
// name@version: a new version fails again until someone re-checks it.
const OVERRIDES = new Map([
  ["khroma@2.1.0", "MIT"], // license: "The MIT License (MIT)", Fabio Spampinato, Andrew Maney
  ["zigpty@0.2.1", "MIT"], // LICENSE: "MIT License", Pooya Parsa
]);

// Weak, file-level copyleft that Slidev 53 cannot run without: Vite 8 depends on lightningcss, mermaid on
// elkjs. Shipped unmodified, run in their own Node process, listed apart in THIRD_PARTY_LICENSES.txt with
// their source: the npm package, or for lightningcss' per-platform binding packages (lightningcss-<platform>,
// a compiled binary only) the tagged source repository. Keyed by exact name@version (the bindings by name
// prefix + version) and the licence they must still declare.
const npmSource = (pkg) => `the npm package https://registry.npmjs.org/${pkg.name}/-/${pkg.name.split("/").pop()}-${pkg.version}.tgz` +
  `${pkg.repository ? ` (repository: ${typeof pkg.repository === "string" ? pkg.repository : pkg.repository.url})` : ""}`;
const EXCEPTIONS = [
  { name: (n) => n === "lightningcss", version: "1.33.0", license: "MPL-2.0", source: npmSource },
  { name: (n) => n.startsWith("lightningcss-"), version: "1.33.0", license: "MPL-2.0",
    source: (pkg) => `the source repository at the release tag, https://github.com/parcel-bundler/lightningcss/tree/v${pkg.version}` +
      ` (this package is its compiled binary for one platform)` },
  { name: (n) => n === "elkjs", version: "0.9.3", license: "EPL-2.0", source: npmSource },
];
const exceptionFor = (pkg, lic) => EXCEPTIONS.find((e) => e.name(pkg.name) && pkg.version === e.version && lic === e.license);

// The licence file of a package: LICENSE, LICENCE.md, LICENSE-MIT, LICENSE-MIT.txt, COPYING, ...; the
// shortest name when there are several. Code files that merely start with "license" don't count.
function licenseFile(dir) {
  return readdirSync(dir)
    .filter((f) => /^(licen[cs]e|copying)([-.][\w.-]*)?$/i.test(f) && !/\.(c?js|mjs|ts|json|map)$/i.test(f))
    .filter((f) => statSync(join(dir, f)).isFile())
    .sort((a, b) => a.length - b.length || a.localeCompare(b))[0];
}

const bad = [];
const parts = [];
const weak = [];
for (const [dir, pkg] of [...seen].sort((a, b) => a[1].name.localeCompare(b[1].name) || a[0].localeCompare(b[0]))) {
  const declared = typeof pkg.license === "string" ? pkg.license : pkg.license?.type;
  const lic = declared || OVERRIDES.get(`${pkg.name}@${pkg.version}`) || "UNKNOWN";
  const ids = lic.replace(/[()]/g, "").split(/\s+(?:OR|AND)\s+/);
  // "A OR B": one allowed is enough; "A AND B": all must be
  const ok = / AND /.test(lic) ? ids.every((i) => ALLOWED.has(i)) : ids.some((i) => ALLOWED.has(i));
  const file = licenseFile(dir);
  const text = file ? readFileSync(join(dir, file), "utf8").trim() : "(no licence file in the package)";
  if (ok) {
    parts.push(`${pkg.name} ${pkg.version} (${lic}${declared ? "" : ", from its licence file"})\n${"-".repeat(60)}\n${text}\n`);
  } else if (exceptionFor(pkg, lic)) {
    weak.push({ id: `${pkg.name}@${pkg.version}: ${lic}`,
      text: `${pkg.name} ${pkg.version} (${lic})\nSource: ${exceptionFor(pkg, lic).source(pkg)}\n${"-".repeat(60)}\n${text}\n` });
  } else {
    bad.push(`${pkg.name}@${pkg.version}: ${lic}`);
  }
}
if (bad.length) {
  console.error("not on the allowed licence list (Stagepack ships only permissively licensed components):\n  " + [...new Set(bad)].join("\n  "));
  process.exit(1);
}
const weakIds = [...new Set(weak.map((w) => w.id))];
writeFileSync(join(top, "THIRD_PARTY_LICENSES.txt"),
  "The packages in the Stagepack runtime (Slidev and what it needs), and their licences.\n\n" + parts.join("\n") +
  (weak.length ? `\n${"=".repeat(60)}\nWeak-copyleft components (unmodified)\n${"=".repeat(60)}\n` +
    "These are shipped exactly as published on npm, unmodified; where to get their source is named below.\n\n" +
    weak.map((w) => w.text).join("\n") : ""));
if (weakIds.length) console.warn("warning: weak-copyleft components (unmodified, allowed by exception):\n  " + weakIds.join("\n  "));
console.log(`${seen.size} packages, ${seen.size - weak.length} permissive, ${weak.length} by exception: ${top}/THIRD_PARTY_LICENSES.txt`);
