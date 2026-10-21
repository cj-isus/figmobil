import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import test from 'node:test';
import { validateProjectSpec, validateProjectTree } from './check.mjs';

function fixtureSpec() {
  return {
    schemaVersion: 1, project: 'figmobil-fixture', phase: 'planning', proposal: 'authorized-workflow',
    implementationApproved: false, approvalEvidence: null,
    environment: { firstPc: { figma: 'desktop', plugins: 'unknown', additionalInstalls: 'ask', os: 'unknown' }, secondPc: { additionalInstalls: 'ask', os: 'unknown' }, transport: 'unresolved', initialDirection: 'first-to-second' },
    boundaries: { covertTransfer: false, disguisedPurpose: false, guaranteedRemoteErasure: false, automaticRemoteGit: false, newInstallsApproved: false },
    documents: ['README.md', 'docs/PLAN.md'],
    requirements: [{ id: 'R-01', text: 'Approved scope', disposition: 'required', acceptanceIds: ['A-00'] }],
    acceptance: [{ id: 'A-00', text: 'Scope agreed', status: 'planned', verification: 'Explicit user decision' }],
    phases: [
      { id: 'P0', name: 'Agree', status: 'active', dependsOn: [], acceptanceIds: ['A-00'] },
      { id: 'P1', name: 'Validate', status: 'not-started', dependsOn: ['P0'], acceptanceIds: ['A-00'] },
    ],
    features: [{ id: 'F-01', name: 'Preview', priority: 'mvp', phaseId: 'P1', acceptanceIds: ['A-00'] }],
    pendingDecisions: [{ id: 'Q-01', question: 'Approve scope?', blocks: ['P1'] }],
  };
}

function hasError(result, id) { assert.equal(result.ok, false); assert.ok(result.errors.some(error => error.id === id), JSON.stringify(result.errors)); }

function temporaryTree(t, spec) {
  const tempBase = realpathSync(tmpdir());
  const root = mkdtempSync(resolve(tempBase, 'figmobil-check-'));
  // Verify the exact cleanup target; never remove a computed project path.
  const rel = relative(tempBase, realpathSync(root));
  assert.ok(rel.startsWith('figmobil-check-') && !rel.includes('..') && !isAbsolute(rel));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const files = [
    ...spec.documents, 'AGENTS.md', 'docs/MAP.md', 'docs/LESSONS.md', 'docs/HARNESS.md',
    'scripts/harness/map.mjs', 'scripts/harness/doctor.mjs', 'scripts/harness/project-memory.mjs',
    'scripts/harness/verify-skills.mjs', '.agents/skills/repo-recon/SKILL.md', '.agents/skills/project-memory/SKILL.md',
  ];
  for (const file of files) {
    mkdirSync(dirname(resolve(root, file)), { recursive: true });
    writeFileSync(resolve(root, file), file === 'README.md' ? '[Plan](docs/PLAN.md)\n' : '# Fixture\n');
  }
  const managedBlock = '<!-- project-harness:start -->\n# Managed fixture\n<!-- project-harness:end -->';
  writeFileSync(resolve(root, 'AGENTS.md'), `${managedBlock}\n\n# Project-specific additions\n`);
  const hashes = Object.fromEntries(['scripts/harness/map.mjs', 'AGENTS.md', 'docs/STATE.md', 'docs/LESSONS.md'].map(file => [file, '0'.repeat(64)]));
  hashes['scripts/harness/map.mjs'] = createHash('sha256').update(readFileSync(resolve(root, 'scripts/harness/map.mjs'))).digest('hex');
  mkdirSync(resolve(root, '.harness'), { recursive: true });
  writeFileSync(resolve(root, '.harness/installed.json'), JSON.stringify({ version: 1, agentsBlockHash: createHash('sha256').update(managedBlock).digest('hex'), files: hashes }));
  return root;
}

test('planning spec and temporary tree pass without implementation approval', t => {
  const spec = fixtureSpec();
  assert.equal(validateProjectSpec(spec).ok, true);
  assert.equal(validateProjectTree(temporaryTree(t, spec), spec).ok, true);
});

test('approved next phase needs evidence and satisfied dependencies', () => {
  const spec = fixtureSpec();
  spec.implementationApproved = true;
  spec.approvalEvidence = 'User explicitly agreed to scope in the fixture.';
  spec.acceptance[0] = { ...spec.acceptance[0], status: 'passed', evidence: 'Scope decision fixture.' };
  spec.pendingDecisions = [];
  spec.phases[0].status = 'complete';
  spec.phases[1].status = 'active';
  assert.equal(validateProjectSpec(spec).ok, true);
  spec.phases[0].status = 'active';
  hasError(validateProjectSpec(spec), 'unsatisfied-dependency');
});

test('approved environment phase allows decisions that block only a later phase', () => {
  const spec = fixtureSpec();
  spec.implementationApproved = true;
  spec.approvalEvidence = 'User approved the scope and channel.';
  spec.acceptance[0] = { ...spec.acceptance[0], status: 'passed', evidence: 'Scope and channel decision fixture.' };
  spec.phases[0].status = 'complete';
  spec.phases[1].status = 'active';
  spec.phases.push({ id: 'P2', name: 'Implement', status: 'not-started', dependsOn: ['P1'], acceptanceIds: ['A-00'] });
  spec.pendingDecisions = [{ id: 'Q-03', question: 'Which exact tools are permitted?', blocks: ['P2'] }];
  assert.equal(validateProjectSpec(spec).ok, true);
  spec.phases[1].status = 'complete';
  spec.phases[2].status = 'active';
  const result = validateProjectSpec(spec);
  hasError(result, 'pending-phase-decision');
  assert.equal(result.errors.some(error => error.id === 'unsatisfied-dependency'), false);
});

test('approved scope cannot admit application files before P2 readiness', t => {
  const spec = fixtureSpec();
  spec.implementationApproved = true;
  spec.approvalEvidence = 'Explicit scope decision fixture.';
  spec.acceptance[0] = { ...spec.acceptance[0], status: 'passed', evidence: 'Scope decision fixture.' };
  spec.acceptance.push({ id: 'A-06', text: 'Environment checked', status: 'planned', verification: 'Environment report' });
  spec.phases[0].status = 'complete';
  spec.phases[1] = { ...spec.phases[1], status: 'active', acceptanceIds: ['A-06'] };
  spec.phases.push({ id: 'P2', name: 'Implement', status: 'not-started', dependsOn: ['P1'], acceptanceIds: ['A-00'] });
  spec.pendingDecisions = [{ id: 'Q-03', question: 'Exact tool permission?', blocks: ['P2'] }];
  assert.equal(validateProjectSpec(spec).ok, true);
  const root = temporaryTree(t, spec);
  mkdirSync(resolve(root, 'src'));
  hasError(validateProjectTree(root, spec), 'premature-implementation');

  spec.acceptance[1] = { ...spec.acceptance[1], status: 'passed', evidence: 'Completed environment report fixture.' };
  spec.phases[1].status = 'complete';
  spec.pendingDecisions = [];
  // Research completion alone must not silently start implementation.
  hasError(validateProjectTree(root, spec), 'premature-implementation');
  spec.phases[2].status = 'active';
  assert.equal(validateProjectTree(root, spec).ok, true);
  spec.phases[2].status = 'blocked';
  assert.equal(validateProjectTree(root, spec).ok, true);
  spec.pendingDecisions = [{ id: 'Q-03', question: 'New prerequisite unresolved?', blocks: ['P2'] }];
  hasError(validateProjectTree(root, spec), 'premature-implementation');
});

test('mandatory acceptance must be scheduled, with later integration verification allowed', () => {
  const spec = fixtureSpec();
  spec.implementationApproved = true;
  spec.approvalEvidence = 'Scope decision fixture.';
  spec.pendingDecisions = [];
  spec.acceptance[0] = { ...spec.acceptance[0], status: 'passed', evidence: 'Scope decision fixture.' };
  for (const phase of spec.phases) phase.status = 'complete';
  spec.acceptance.push({ id: 'A-extra', text: 'Required behavior', status: 'planned', verification: 'Real behavior check' });
  spec.features.push({ id: 'F-extra', name: 'Mandatory behavior', priority: 'mvp', phaseId: 'P1', acceptanceIds: ['A-extra'] });
  hasError(validateProjectSpec(spec), 'unscheduled-acceptance');

  spec.phases.push({ id: 'P2', name: 'Later integration', status: 'not-started', dependsOn: ['P1'], acceptanceIds: ['A-extra'] });
  assert.equal(validateProjectSpec(spec).ok, true);
  spec.phases[2].status = 'complete';
  hasError(validateProjectSpec(spec), 'incomplete-acceptance');

  spec.features.pop();
  spec.phases.pop();
  spec.requirements.push({ id: 'R-extra', text: 'Required behavior', disposition: 'required', acceptanceIds: ['A-extra'] });
  hasError(validateProjectSpec(spec), 'unscheduled-acceptance');
  spec.requirements.at(-1).disposition = 'accepted';
  hasError(validateProjectSpec(spec), 'unscheduled-acceptance');
  spec.requirements.at(-1).disposition = 'proposed';
  assert.equal(validateProjectSpec(spec).ok, true);
});

test('rejects wrong types, duplicate IDs, dangling acceptance and phase IDs', () => {
  const spec = fixtureSpec();
  spec.schemaVersion = '1';
  spec.requirements.push({ ...spec.requirements[0] });
  spec.features[0].phaseId = 'P9';
  spec.requirements[0].acceptanceIds = ['A-missing'];
  spec.pendingDecisions[0].blocks = ['P9'];
  const result = validateProjectSpec(spec);
  for (const id of ['schema-version', 'duplicate-id', 'dangling-reference', 'feature-phase']) hasError(result, id);
  hasError(validateProjectSpec(null), 'schema');
});

test('rejects phase dependency cycles and missing dependencies', () => {
  const spec = fixtureSpec();
  spec.phases[0].dependsOn = ['P1'];
  hasError(validateProjectSpec(spec), 'phase-cycle');
  spec.phases[0].dependsOn = [];
  spec.phases[1].dependsOn = ['P9'];
  hasError(validateProjectSpec(spec), 'dangling-reference');
});

test('rejects missing approval, passed acceptance and waiver evidence', () => {
  const spec = fixtureSpec();
  spec.implementationApproved = true;
  spec.approvalEvidence = ' ';
  const result = validateProjectSpec(spec);
  for (const id of ['approval-evidence', 'scope-not-passed']) hasError(result, id);
  spec.acceptance[0].status = 'passed';
  hasError(validateProjectSpec(spec), 'acceptance-evidence');
  spec.acceptance[0].status = 'waived';
  hasError(validateProjectSpec(spec), 'waiver-reason');
  spec.implementationApproved = false;
  hasError(validateProjectSpec(spec), 'unexpected-approval-evidence');
});

test('rejects premature phase advancement and premature phase completion', () => {
  const spec = fixtureSpec();
  spec.phases[1].status = 'active';
  const result = validateProjectSpec(spec);
  for (const id of ['premature-phase', 'unsatisfied-dependency', 'pending-phase-decision']) hasError(result, id);
  spec.phases[0].status = 'complete';
  hasError(validateProjectSpec(spec), 'incomplete-acceptance');
});

test('fixed application roots cannot be bypassed by custom roots in the manifest', t => {
  const spec = fixtureSpec();
  spec.applicationRoots = ['somewhere-else'];
  const root = temporaryTree(t, spec);
  for (const path of ['src', 'apps', 'packages']) mkdirSync(resolve(root, path));
  mkdirSync(resolve(root, 'plugin'));
  writeFileSync(resolve(root, 'plugin/manifest.json'), '{}');
  const result = validateProjectTree(root, spec);
  assert.equal(result.errors.filter(error => error.id === 'premature-implementation').length, 4);
});

test('rejects missing files, broken Markdown links and changed managed content', t => {
  const spec = fixtureSpec(), root = temporaryTree(t, spec);
  spec.documents.push('../outside.md', 'docs/MISSING.md');
  writeFileSync(resolve(root, 'docs/PLAN.md'), '[Broken](./no-such-file.md)\n[External](https://example.invalid)\n');
  writeFileSync(resolve(root, 'scripts/harness/map.mjs'), '// modified\n');
  const result = validateProjectTree(root, spec);
  for (const id of ['outside-project', 'required-file', 'broken-link', 'managed-file-changed']) hasError(result, id);
});

test('Go application roots are also rejected before implementation readiness', t => {
  const spec = fixtureSpec(), root = temporaryTree(t, spec);
  mkdirSync(resolve(root, 'cmd'));
  mkdirSync(resolve(root, 'internal'));
  writeFileSync(resolve(root, 'go.mod'), 'module fixture\n');
  const result = validateProjectTree(root, spec);
  assert.equal(result.errors.filter(error => error.id === 'premature-implementation').length, 3);
});

test('managed block is checked while custom AGENTS and editable lessons are allowed', t => {
  const spec = fixtureSpec(), root = temporaryTree(t, spec);
  writeFileSync(resolve(root, 'docs/LESSONS.md'), '# User lessons changed\n');
  assert.equal(validateProjectTree(root, spec).ok, true);
  writeFileSync(resolve(root, 'AGENTS.md'), '<!-- project-harness:start -->\nChanged\n<!-- project-harness:end -->');
  hasError(validateProjectTree(root, spec), 'managed-agents-changed');
});
