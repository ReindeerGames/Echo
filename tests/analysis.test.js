const test = require('node:test');
const assert = require('node:assert/strict');

const { runDeterministicChecks, detectChanges } = require('../src/analysis');

test('runDeterministicChecks finds stopped, unhealthy, restart, cpu, and memory issues', () => {
  const issues = runDeterministicChecks(
    [
      {
        id: 'c1',
        name: 'api',
        group: 'app',
        state: 'exited',
        statusText: 'Exited (1) 1 minute ago',
        health: 'unhealthy',
        restarting: true,
        cpuPct: 97,
        memPct: 96
      }
    ],
    {
      cpuHighPct: 85,
      memHighPct: 85
    }
  );

  const types = new Set(issues.map((item) => item.type));
  assert.equal(types.has('container_stopped'), true);
  assert.equal(types.has('container_restarting'), true);
  assert.equal(types.has('container_unhealthy'), true);
  assert.equal(types.has('cpu_high'), true);
  assert.equal(types.has('memory_high'), true);
});

test('runDeterministicChecks applies medium severity below 95%', () => {
  const issues = runDeterministicChecks(
    [
      {
        id: 'c2',
        name: 'worker',
        group: 'jobs',
        state: 'running',
        statusText: 'Up 3 minutes',
        health: 'healthy',
        restarting: false,
        cpuPct: 86,
        memPct: 88
      }
    ],
    {
      cpuHighPct: 85,
      memHighPct: 85
    }
  );

  const cpuIssue = issues.find((item) => item.type === 'cpu_high');
  const memIssue = issues.find((item) => item.type === 'memory_high');
  assert.equal(cpuIssue.severity, 'medium');
  assert.equal(memIssue.severity, 'medium');
});

test('detectChanges identifies state, health, cpu and memory deltas', () => {
  const previous = new Map();
  previous.set('c1', {
    container_id: 'c1',
    container_name: 'api',
    state: 'running',
    health: 'healthy',
    cpu_pct: 10,
    mem_pct: 20
  });

  const changes = detectChanges(
    [
      {
        id: 'c1',
        name: 'api',
        state: 'restarting',
        health: 'unhealthy',
        cpuPct: 60,
        memPct: 70
      }
    ],
    previous,
    {
      cpuChangePct: 25,
      memChangePct: 25
    }
  );

  const changeTypes = new Set(changes.map((item) => item.type));
  assert.equal(changeTypes.has('state_change'), true);
  assert.equal(changeTypes.has('health_change'), true);
  assert.equal(changeTypes.has('cpu_change'), true);
  assert.equal(changeTypes.has('memory_change'), true);
});

test('detectChanges returns empty list when no previous snapshot exists', () => {
  const changes = detectChanges(
    [
      {
        id: 'c1',
        name: 'api',
        state: 'running',
        health: 'healthy',
        cpuPct: 10,
        memPct: 20
      }
    ],
    new Map(),
    {
      cpuChangePct: 25,
      memChangePct: 25
    }
  );

  assert.deepEqual(changes, []);
});
