const test = require('node:test');
const assert = require('node:assert/strict');

const { filterLogs } = require('../src/logs');

test('filterLogs deduplicates similar lines with varying timestamps and ids', () => {
  const input = [
    '2026-04-01T10:00:00Z ERROR connection refused request=aaaaaaaaaaaaaaaa',
    '2026-04-01T10:00:01Z ERROR connection refused request=bbbbbbbbbbbbbbbb'
  ];

  const result = filterLogs(input, { maxLines: 10, maxChars: 200 });

  assert.equal(result.stats.inputLines, 2);
  assert.equal(result.stats.dedupedLines, 1);
  assert.equal(result.lines.length, 1);
});

test('filterLogs prioritizes high-signal lines and enforces maxLines', () => {
  const input = [
    'INFO boot complete',
    'WARN retrying dependency',
    'ERROR database timeout',
    'FATAL out of memory crash'
  ];

  const result = filterLogs(input, { maxLines: 2, maxChars: 200 });
  assert.equal(result.lines.length, 2);
  assert.equal(result.lines.some((line) => /fatal/i.test(line)), true);
  assert.equal(result.lines.some((line) => /error/i.test(line)), true);
});

test('filterLogs truncates overlong lines', () => {
  const longLine = `ERROR ${'x'.repeat(50)}`;
  const result = filterLogs([longLine], { maxLines: 10, maxChars: 20 });
  assert.equal(result.lines[0].length <= 20, true);
  assert.equal(result.lines[0].endsWith('...'), true);
});
