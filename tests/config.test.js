const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const { loadJsonFile } = require('../src/config');

test('loadJsonFile parses valid JSON', () => {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'echo-config-'));
  const filePath = path.join(tempDir, 'config.json');
  fs.writeFileSync(filePath, '{"ok":true}', 'utf8');

  const data = loadJsonFile(filePath, {});
  assert.deepEqual(data, { ok: true });

  fs.rmSync(tempDir, { recursive: true, force: true });
});

test('loadJsonFile returns fallback on missing file or invalid JSON', () => {
  const fallback = { safe: true };
  const missing = loadJsonFile('/definitely/missing/config.json', fallback);
  assert.deepEqual(missing, fallback);

  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'echo-config-bad-'));
  const badPath = path.join(tempDir, 'bad.json');
  fs.writeFileSync(badPath, '{bad json', 'utf8');

  const bad = loadJsonFile(badPath, fallback);
  assert.deepEqual(bad, fallback);

  fs.rmSync(tempDir, { recursive: true, force: true });
});
