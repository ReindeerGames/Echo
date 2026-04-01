const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const { initState } = require('../src/state');

function createTempDbPath() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'echo-state-'));
  return {
    dir,
    dbPath: path.join(dir, 'echo.db')
  };
}

test('state tracks recent queries and similarity counts', () => {
  const { dir, dbPath } = createTempDbPath();
  const state = initState(dbPath);

  state.saveRecentQuery({
    sender: '27820000001',
    intent: 'full_report',
    target: null,
    message: 'full report',
    normalizedMessage: 'full report',
    response: 'ok'
  });
  state.saveRecentQuery({
    sender: '27820000001',
    intent: 'full_report',
    target: null,
    message: 'full report',
    normalizedMessage: 'full report',
    response: 'ok'
  });

  assert.equal(state.countSimilarQueries('full report', 1440), 2);

  state.db.close();
  fs.rmSync(dir, { recursive: true, force: true });
});

test('state stores snapshots and resolves latest/previous maps', () => {
  const { dir, dbPath } = createTempDbPath();
  const state = initState(dbPath);

  const firstTs = new Date('2026-01-01T00:00:00Z').toISOString();
  const secondTs = new Date('2026-01-01T00:05:00Z').toISOString();

  state.saveSnapshots(
    [
      {
        id: 'c1',
        name: 'api',
        image: 'my/api:1',
        group: 'app',
        state: 'running',
        health: 'healthy',
        cpuPct: 10,
        memBytes: 1000,
        memLimit: 10000,
        memPct: 10
      }
    ],
    firstTs
  );

  state.saveSnapshots(
    [
      {
        id: 'c1',
        name: 'api',
        image: 'my/api:1',
        group: 'app',
        state: 'restarting',
        health: 'unhealthy',
        cpuPct: 50,
        memBytes: 6000,
        memLimit: 10000,
        memPct: 60
      }
    ],
    secondTs
  );

  const latest = state.getLatestSnapshotMap();
  const previous = state.getPreviousSnapshotMap();

  assert.equal(latest.get('c1').state, 'restarting');
  assert.equal(previous.get('c1').state, 'running');

  state.db.close();
  fs.rmSync(dir, { recursive: true, force: true });
});

test('state aggregates AI usage and cost summary', () => {
  const { dir, dbPath } = createTempDbPath();
  const state = initState(dbPath);

  state.saveAIUsageEvent({
    sender: '27820000001',
    intent: 'full_report',
    model: 'gpt-4o-mini',
    status: 'ok',
    usage: {
      inputTokens: 1000,
      cachedInputTokens: 200,
      outputTokens: 300,
      reasoningTokens: 0,
      totalTokens: 1300
    }
  });

  const summary = state.getAIUsageSummary();
  assert.equal(summary.all.calls, 1);
  assert.equal(summary.all.inputTokens, 1000);
  assert.equal(summary.all.outputTokens, 300);
  assert.equal(summary.all.totalCostUsd > 0, true);

  state.db.close();
  fs.rmSync(dir, { recursive: true, force: true });
});
