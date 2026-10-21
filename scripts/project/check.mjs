#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { existsSync, lstatSync, readFileSync } from 'node:fs';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const NOTE = 'Offline advisory workflow checks only. They cannot authenticate human consent or enforce security; passing does not verify application behavior.';
const HARNESS_FILES = [
  'AGENTS.md', 'docs/MAP.md', 'docs/LESSONS.md', 'docs/HARNESS.md',
  'scripts/harness/map.mjs', 'scripts/harness/doctor.mjs',
  'scripts/harness/project-memory.mjs', 'scripts/harness/verify-skills.mjs',
  '.agents/skills/repo-recon/SKILL.md', '.agents/skills/project-memory/SKILL.md',
];
const GENERIC_DOCS = new Set(['AGENTS.md', 'docs/MAP.md', 'docs/HARNESS.md', 'docs/MEMORY.md', 'docs/SKILLS-CATALOG.md', 'docs/ONBOARDING.md']);
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const text = value => typeof value === 'string' && value.trim().length > 0;
const evidence = value => text(value) || (Array.isArray(value) && value.length > 0 && value.every(text));
const sha256 = value => createHash('sha256').update(value).digest('hex');

function report() {
  const checks = [], errors = [];
  return {
    expect(condition, id, message) { if (!condition) errors.push({ id, message }); },
    section(id, run) {
      const before = errors.length;
      run();
      checks.push({ id, ok: errors.length === before });
    },
    result() { return { ok: errors.length === 0, checks, errors, note: NOTE }; },
  };
}

/** Deterministic validation of planning data, without filesystem or side effects. */
export function validateProjectSpec(spec) {
  const r = report();
  if (!object(spec)) {
    r.section('schema', () => r.expect(false, 'schema', 'Project specification must be an object.'));
    return r.result();
  }
  const groups = ['requirements', 'acceptance', 'phases', 'features', 'pendingDecisions'];
  const records = Object.fromEntries(groups.map(name => [name, Array.isArray(spec[name]) ? spec[name].filter(object) : []]));
  const byId = name => new Map(records[name].filter(item => text(item.id)).map(item => [item.id, item]));
  const acceptance = byId('acceptance'), phases = byId('phases');
  const refs = (value, label, target, allowEmpty = false) => {
    r.expect(Array.isArray(value) && (allowEmpty || value.length > 0) && value.every(text), 'reference-type', `${label} must be ${allowEmpty ? 'an' : 'a nonempty'} array of IDs.`);
    if (!Array.isArray(value)) return;
    r.expect(new Set(value).size === value.length, 'duplicate-reference', `${label} has duplicate IDs.`);
    for (const id of value) r.expect(target.has(id), 'dangling-reference', `${label} references missing ID ${String(id)}.`);
  };

  r.section('schema', () => {
    r.expect(spec.schemaVersion === 1, 'schema-version', 'schemaVersion must be 1.');
    for (const field of ['project', 'phase', 'proposal']) r.expect(text(spec[field]), 'required-text', `${field} must be nonempty text.`);
    r.expect(typeof spec.implementationApproved === 'boolean', 'approval-type', 'implementationApproved must be boolean.');
    r.expect(Array.isArray(spec.documents) && spec.documents.length > 0 && spec.documents.every(text), 'documents-type', 'documents must be a nonempty array of paths.');
    if (Array.isArray(spec.documents)) r.expect(new Set(spec.documents).size === spec.documents.length, 'duplicate-document', 'documents contains duplicate paths.');
    r.expect(object(spec.environment), 'environment-type', 'environment must be an object.');
    if (object(spec.environment)) {
      for (const pc of ['firstPc', 'secondPc']) {
        r.expect(object(spec.environment[pc]), 'environment-type', `environment.${pc} must be an object.`);
        for (const field of pc === 'firstPc' ? ['figma', 'plugins', 'additionalInstalls', 'os'] : ['additionalInstalls', 'os']) {
          r.expect(text(spec.environment[pc]?.[field]), 'environment-text', `environment.${pc}.${field} must be text.`);
        }
      }
      for (const field of ['transport', 'initialDirection']) r.expect(text(spec.environment[field]), 'environment-text', `environment.${field} must be text.`);
    }
    r.expect(object(spec.boundaries), 'boundaries-type', 'boundaries must be an object.');
    for (const field of ['covertTransfer', 'disguisedPurpose', 'guaranteedRemoteErasure', 'automaticRemoteGit', 'newInstallsApproved']) {
      r.expect(typeof spec.boundaries?.[field] === 'boolean', 'boundary-type', `boundaries.${field} must be boolean.`);
    }
  });

  r.section('records', () => {
    const ids = new Set();
    for (const name of groups) {
      r.expect(Array.isArray(spec[name]) && (name === 'pendingDecisions' || spec[name].length > 0), 'collection-type', `${name} must be an array${name === 'pendingDecisions' ? '' : ' with at least one record'}.`);
      if (!Array.isArray(spec[name])) continue;
      for (const item of spec[name]) {
        r.expect(object(item), 'record-type', `${name} contains a non-object record.`);
        if (!object(item)) continue;
        r.expect(text(item.id), 'record-id', `${name} record needs a nonempty ID.`);
        r.expect(!ids.has(item.id), 'duplicate-id', `Duplicate ID ${String(item.id)}.`);
        ids.add(item.id);
        const label = name === 'features' || name === 'phases' ? 'name' : name === 'pendingDecisions' ? 'question' : 'text';
        r.expect(text(item[label]), 'record-text', `${item.id ?? name}.${label} must be text.`);
      }
    }
    for (const item of records.requirements) r.expect(['proposed', 'needs-decision', 'needs-redefinition', 'unavailable', 'excluded', 'required', 'accepted'].includes(item.disposition), 'requirement-disposition', `${item.id} has invalid disposition.`);
    for (const item of records.features) r.expect(['mvp', 'later', 'optional'].includes(item.priority), 'feature-priority', `${item.id} has invalid priority.`);
    for (const item of records.acceptance) {
      r.expect(['planned', 'passed', 'waived', 'failed'].includes(item.status), 'acceptance-status', `${item.id} has invalid acceptance status.`);
      r.expect(text(item.verification), 'verification', `${item.id} needs a verification description.`);
      if (item.status === 'passed') r.expect(evidence(item.evidence), 'acceptance-evidence', `${item.id} passed without nonempty evidence.`);
      if (item.status === 'waived') r.expect(text(item.reason), 'waiver-reason', `${item.id} waived without a reason.`);
    }
    for (const item of records.phases) r.expect(['not-started', 'active', 'complete', 'blocked'].includes(item.status), 'phase-status', `${item.id} has invalid phase status.`);
  });

  r.section('references', () => {
    r.expect(phases.has('P0'), 'initial-phase', 'Initial phase P0 is required.');
    r.expect(acceptance.has('A-00'), 'scope-acceptance', 'Scope acceptance A-00 is required.');
    for (const name of ['requirements', 'features', 'phases']) for (const item of records[name]) refs(item.acceptanceIds, `${item.id}.acceptanceIds`, acceptance);
    for (const item of records.features) {
      r.expect(item.phaseId === null || (text(item.phaseId) && phases.has(item.phaseId)), 'feature-phase', `${item.id}.phaseId must be null or an existing phase ID.`);
      if (item.priority === 'mvp') r.expect(phases.has(item.phaseId), 'mvp-phase', `${item.id} MVP feature needs a phase.`);
    }
    for (const item of records.phases) refs(item.dependsOn, `${item.id}.dependsOn`, phases, true);
    for (const item of records.pendingDecisions) refs(item.blocks, `${item.id}.blocks`, phases);
    const scheduledAcceptance = new Set(records.phases.flatMap(item => Array.isArray(item.acceptanceIds) ? item.acceptanceIds : []));
    const requiredRecords = [
      ...records.requirements.filter(item => ['required', 'accepted'].includes(item.disposition)),
      ...records.features.filter(item => item.priority === 'mvp'),
    ];
    for (const item of requiredRecords) for (const id of Array.isArray(item.acceptanceIds) ? item.acceptanceIds : []) {
      r.expect(scheduledAcceptance.has(id), 'unscheduled-acceptance', `${item.id} requires ${String(id)}, but no phase schedules its verification.`);
    }
  });

  r.section('phase-graph', () => {
    const visited = new Set(), visiting = new Set();
    function visit(id) {
      if (visiting.has(id)) { r.expect(false, 'phase-cycle', `Phase dependency cycle includes ${id}.`); return; }
      if (visited.has(id) || !phases.has(id)) return;
      visiting.add(id);
      const dependencies = phases.get(id).dependsOn;
      for (const dependency of Array.isArray(dependencies) ? dependencies : []) visit(dependency);
      visiting.delete(id);
      visited.add(id);
    }
    for (const id of phases.keys()) visit(id);
  });

  r.section('approval', () => {
    if (spec.implementationApproved === false) r.expect(spec.approvalEvidence === null, 'unexpected-approval-evidence', 'Unapproved scope must have approvalEvidence: null.');
    if (spec.implementationApproved === true) {
      r.expect(evidence(spec.approvalEvidence), 'approval-evidence', 'Approved scope needs nonempty approvalEvidence.');
      r.expect(acceptance.get('A-00')?.status === 'passed', 'scope-not-passed', 'Approved implementation requires A-00 passed.');
    }
    if (acceptance.get('A-00')?.status === 'passed') r.expect(spec.implementationApproved === true, 'scope-without-approval', 'A-00 cannot pass while implementationApproved is false.');
  });

  r.section('phase-progress', () => {
    for (const phase of records.phases) {
      if (!['active', 'complete'].includes(phase.status)) continue;
      if (phase.id !== 'P0') r.expect(spec.implementationApproved === true && acceptance.get('A-00')?.status === 'passed', 'premature-phase', `${phase.id} cannot advance before scope approval.`);
      for (const dependency of Array.isArray(phase.dependsOn) ? phase.dependsOn : []) r.expect(phases.get(dependency)?.status === 'complete', 'unsatisfied-dependency', `${phase.id} requires completed ${dependency}.`);
      for (const decision of records.pendingDecisions) if (Array.isArray(decision.blocks) && decision.blocks.includes(phase.id)) r.expect(false, 'pending-phase-decision', `${phase.id} is blocked by ${decision.id}.`);
      if (phase.status === 'complete') for (const id of Array.isArray(phase.acceptanceIds) ? phase.acceptanceIds : []) r.expect(['passed', 'waived'].includes(acceptance.get(id)?.status), 'incomplete-acceptance', `${phase.id} cannot complete before ${id} passes or is explicitly waived.`);
    }
  });
  return r.result();
}

function inside(root, path) {
  if (!text(path) || isAbsolute(path) || /^[a-z]:/i.test(path)) return null;
  const target = resolve(root, path), rel = relative(root, target);
  return rel === '..' || rel.startsWith('../') || rel.startsWith('..\\') || isAbsolute(rel) ? null : target;
}

function markdownTargets(markdown) {
  const source = markdown.replace(/```[\s\S]*?```|~~~[\s\S]*?~~~/g, '');
  return [
    ...source.matchAll(/!?\[[^\]]*\]\(\s*(?:<([^>]+)>|([^\s)]+))(?:\s+["'][^"']*["'])?\s*\)/g),
    ...source.matchAll(/^\s{0,3}\[[^\]]+\]:\s*(?:<([^>]+)>|(\S+))/gm),
  ].map(match => match[1] ?? match[2]);
}

/** Read-only project checks. No commands or source files are executed. */
export function validateProjectTree(root, spec) {
  const r = report(), base = resolve(root);
  const documents = Array.isArray(spec?.documents) ? spec.documents.filter(text) : [];
  r.section('required-files', () => {
    for (const path of new Set([...HARNESS_FILES, ...documents])) {
      const target = inside(base, path);
      r.expect(target !== null, 'outside-project', `File path must stay inside the project: ${path}.`);
      if (!target) continue;
      try { r.expect(lstatSync(target).isFile(), 'required-file', `Required regular file is missing: ${path}.`); }
      catch { r.expect(false, 'required-file', `Required file is missing or unreadable: ${path}.`); }
    }
  });
  r.section('implementation-boundary', () => {
    const implementation = Array.isArray(spec?.phases) ? spec.phases.find(phase => phase?.id === 'P2') : undefined;
    // Scope agreement permits research, not implementation before environment readiness.
    // A blocked implementation can retain existing code while its defects are repaired.
    const ready = spec?.implementationApproved === true
      && ['active', 'blocked', 'complete'].includes(implementation?.status)
      && Array.isArray(implementation?.dependsOn)
      && implementation.dependsOn.every(id => spec.phases.find(phase => phase?.id === id)?.status === 'complete')
      && Array.isArray(spec.pendingDecisions)
      && !spec.pendingDecisions.some(decision => Array.isArray(decision?.blocks) && decision.blocks.includes('P2'))
      && validateProjectSpec(spec).ok;
    if (ready) return;
    // Fixed conventional roots: a custom manifest must not redefine this check.
    for (const path of ['src', 'apps', 'packages', 'plugin/manifest.json', 'cmd', 'internal', 'go.mod']) {
      let present = false;
      try { lstatSync(resolve(base, path)); present = true; } catch (error) {
        if (error.code !== 'ENOENT') r.expect(false, 'implementation-path-read', `Cannot inspect ${path}: ${error.code}.`);
      }
      r.expect(!present, 'premature-implementation', `Implementation path exists before scope approval and P2 readiness: ${path}.`);
    }
  });
  r.section('markdown-links', () => {
    for (const path of documents.filter(path => path.endsWith('.md') && !GENERIC_DOCS.has(path))) {
      const source = inside(base, path);
      if (!source || !existsSync(source)) continue; // Already reported by required-files.
      try {
        for (const raw of markdownTargets(readFileSync(source, 'utf8'))) {
          if (/^(?:[a-z][a-z\d+.-]*:|\/\/|#)/i.test(raw)) continue;
          const target = decodeURIComponent(raw.split(/[?#]/, 1)[0]);
          if (!target || isAbsolute(target)) continue;
          const resolved = inside(base, relative(base, resolve(dirname(source), target)));
          r.expect(resolved !== null && existsSync(resolved), 'broken-link', `${path} has unresolved relative link: ${raw}.`);
        }
      } catch (error) { r.expect(false, 'markdown-read', `Cannot inspect links in ${path}: ${error.message}.`); }
    }
  });
  r.section('managed-integrity', () => {
    try {
      const manifest = JSON.parse(readFileSync(resolve(base, '.harness/installed.json'), 'utf8'));
      r.expect(manifest.version === 1 && object(manifest.files), 'managed-manifest', 'Invalid managed harness manifest.');
      r.expect(/^[a-f\d]{64}$/.test(manifest.agentsBlockHash), 'managed-hash', 'Missing valid managed AGENTS block hash.');
      for (const [path, hash] of Object.entries(object(manifest.files) ? manifest.files : {})) {
        if (['AGENTS.md', 'docs/STATE.md', 'docs/LESSONS.md'].includes(path)) continue;
        const target = inside(base, path);
        r.expect(target !== null && /^[a-f\d]{64}$/.test(hash), 'managed-path', `Invalid managed file record: ${path}.`);
        if (!target) continue;
        const matches = existsSync(target) && sha256(readFileSync(target)) === hash;
        r.expect(matches, 'managed-file-changed', `Managed file missing or changed: ${path}.`);
      }
      const rules = readFileSync(resolve(base, 'AGENTS.md'), 'utf8');
      const blocks = [...rules.matchAll(/<!-- project-harness:start -->[\s\S]*?<!-- project-harness:end -->/g)];
      const starts = rules.split('<!-- project-harness:start -->').length - 1;
      const ends = rules.split('<!-- project-harness:end -->').length - 1;
      r.expect(starts === 1 && ends === 1 && blocks.length === 1 && sha256(blocks[0][0]) === manifest.agentsBlockHash, 'managed-agents-changed', 'Managed AGENTS block missing, duplicated or changed.');
    } catch (error) { r.expect(false, 'managed-manifest-read', `Cannot verify .harness/installed.json: ${error.message}.`); }
  });
  return r.result();
}

function main() {
  const r = report();
  try {
    const args = process.argv.slice(2);
    if (args.length > 1 || args[0]?.startsWith('--')) throw new Error('Usage: node scripts/project/check.mjs [project-directory]');
    const [major, minor] = process.versions.node.split('.').map(Number);
    r.section('node-version', () => r.expect(major > 22 || (major === 22 && minor >= 13), 'node-version', 'Node >=22.13 is required.'));
    const root = resolve(args[0] ?? process.cwd());
    const spec = JSON.parse(readFileSync(resolve(root, 'spec/project.json'), 'utf8').replace(/^\uFEFF/, ''));
    const schema = validateProjectSpec(spec);
    const tree = schema.ok ? validateProjectTree(root, spec) : { checks: [], errors: [] };
    const runtime = r.result();
    const errors = [...runtime.errors, ...schema.errors, ...tree.errors];
    const result = { ok: errors.length === 0, checks: [...runtime.checks, ...schema.checks, ...tree.checks], errors, note: NOTE };
    console.log(JSON.stringify(result, null, 2));
    process.exitCode = result.ok ? 0 : 1;
  } catch (error) {
    console.log(JSON.stringify({ ok: false, checks: r.result().checks, errors: [{ id: 'check-failed', message: error.message }], note: NOTE }, null, 2));
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) main();
