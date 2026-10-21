#!/usr/bin/env node
// Offline integrity check for the explicitly reviewed skill set, not a malware scanner.
import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, readdirSync, realpathSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
function safeRelative(path) {
  return typeof path === 'string' && path.length > 0 && !path.includes('\\')
    && path.split('/').every(p => p && p !== '.' && p !== '..' && !p.includes(':')) && !path.startsWith('/');
}
function regular(path) {
  const st = lstatSync(path);
  if (st.isSymbolicLink() || !st.isFile()) throw new Error('Non-regular skill file');
  return readFileSync(path);
}
function walk(path, prefix = '') {
  if (lstatSync(path).isSymbolicLink()) throw new Error('Symlink skill directory');
  return readdirSync(path, { withFileTypes: true }).flatMap(e => {
    if (e.isSymbolicLink()) throw new Error('Symlink skill entry');
    const rel = prefix + e.name;
    if (e.isDirectory()) return walk(join(path, e.name), rel + '/');
    if (!e.isFile()) throw new Error('Non-regular skill entry');
    return [rel];
  }).sort();
}
export function verifySkills(projectDir) {
  const root = realpathSync(resolve(projectDir));
  // Check each parent too: an intermediate junction must not redirect the scan.
  for (const p of ['.harness', '.agents', '.agents/skills']) {
    if (lstatSync(join(root, p)).isSymbolicLink()) throw new Error('Symlink skill parent');
  }
  const lock = JSON.parse(regular(join(root, '.harness/skills.lock.json')).toString('utf8'));
  if (lock.version !== 1 || !Array.isArray(lock.skills) || !lock.skills.length) throw new Error('Invalid skill lock');
  const names = new Set(), results = [];
  for (const s of lock.skills) {
    if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(s.name) || names.has(s.name)
      || !/^[\da-f]{40}$/.test(s.ref) || !/^[\w.-]+\/[\w.-]+$/.test(s.repo)
      || !safeRelative(s.sourcePath) || !/^[\da-f]{64}$/.test(s.upstreamSkillSha256)
      || !s.files || typeof s.files !== 'object' || Array.isArray(s.files)) throw new Error('Invalid skill provenance');
    names.add(s.name);
    const keys = Object.keys(s.files).sort();
    if (!keys.includes('SKILL.md') || keys.some(k => !safeRelative(k)
      || !/^[\da-f]{64}$/.test(s.files[k]) || !(k.endsWith('.md') || k === 'LICENSE' || k === 'NOTICE')))
      throw new Error('Invalid reviewed file list');
    const dir = join(root, '.agents/skills', s.name), actual = walk(dir);
    if (JSON.stringify(actual) !== JSON.stringify(keys)) throw new Error(`Skill file set changed: ${s.name}`);
    for (const file of keys) if (sha(regular(join(dir, file))) !== s.files[file]) throw new Error(`Skill bytes changed: ${s.name}/${file}`);
    const entry = regular(join(dir, 'SKILL.md')).toString('utf8');
    if (!entry.startsWith('---\n') || !entry.includes(`\nname: ${s.name}\n`) || !/\ndescription:/.test(entry))
      throw new Error(`Invalid skill entry: ${s.name}`);
    results.push({ name: s.name, ref: s.ref, files: keys.length, entryBytes: Buffer.byteLength(entry) });
  }
  return { ok: true, root, skills: results, note: 'Hashes match the reviewed lock. No network, installs, scripts, model calls or application checks were run. Unlisted skills are outside this check.' };
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length > 3) throw new Error('Usage: verify-skills.mjs [project-dir]');
    console.log(JSON.stringify(verifySkills(process.argv[2] ?? '.'), null, 2));
  } catch (error) { console.log(JSON.stringify({ ok: false, message: error.message })); process.exitCode = 1; }
}
