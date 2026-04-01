const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const { loadEnabledSkills, pickRelevantSkills, normalizeQuery } = require('../src/skills');

test('normalizeQuery strips punctuation and normalizes spaces', () => {
  assert.equal(normalizeQuery('  Logs: API-1??  '), 'logs api 1');
});

test('pickRelevantSkills prioritizes matching skill tokens', () => {
  const skills = [
    { path: 'core/incident-triage.md', content: 'incident workflow' },
    { path: 'core/log-triage.md', content: 'log workflow' },
    { path: 'core/performance-investigation.md', content: 'perf workflow' }
  ];

  const picked = pickRelevantSkills('please run log triage on api', skills, 2);
  assert.equal(picked.length, 2);
  assert.equal(picked[0].path, 'core/log-triage.md');
});

test('loadEnabledSkills reads enabled items from a registry', () => {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'echo-skills-'));
  const skillsDir = path.join(tempDir, 'skills');
  fs.mkdirSync(path.join(skillsDir, 'core'), { recursive: true });

  fs.writeFileSync(
    path.join(skillsDir, 'registry.json'),
    JSON.stringify({ enabled: ['core/log-triage.md'] }),
    'utf8'
  );
  fs.writeFileSync(path.join(skillsDir, 'core', 'log-triage.md'), '# Log Triage\n', 'utf8');

  const loaded = loadEnabledSkills(tempDir);
  assert.equal(loaded.length, 1);
  assert.equal(loaded[0].path, 'core/log-triage.md');
  assert.equal(loaded[0].content.includes('Log Triage'), true);

  fs.rmSync(tempDir, { recursive: true, force: true });
});
