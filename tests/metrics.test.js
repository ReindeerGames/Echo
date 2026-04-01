const test = require('node:test');
const assert = require('node:assert/strict');

const { getTopContainers, detectOutliers } = require('../src/metrics');

test('getTopContainers sorts and limits by numeric metric', () => {
  const top = getTopContainers(
    [
      { name: 'a', cpuPct: 2 },
      { name: 'b', cpuPct: 10 },
      { name: 'c', cpuPct: 7 },
      { name: 'd', cpuPct: null }
    ],
    'cpuPct',
    2
  );

  assert.deepEqual(top.map((item) => item.name), ['b', 'c']);
});

test('detectOutliers emits cpu and mem outliers over z-score and absolute thresholds', () => {
  const outliers = detectOutliers(
    {
      app: [
        { id: '1', name: 'a', cpuPct: 5, memPct: 5 },
        { id: '2', name: 'b', cpuPct: 6, memPct: 6 },
        { id: '3', name: 'c', cpuPct: 7, memPct: 7 },
        { id: '4', name: 'd', cpuPct: 8, memPct: 8 },
        { id: '5', name: 'e', cpuPct: 95, memPct: 95 }
      ]
    },
    {
      outlierZScore: 1.5,
      outlierMinCpuPct: 40,
      outlierMinMemPct: 40
    }
  );

  const metrics = new Set(outliers.filter((item) => item.containerName === 'e').map((item) => item.metric));
  assert.equal(metrics.has('cpu_pct'), true);
  assert.equal(metrics.has('mem_pct'), true);
});

test('detectOutliers ignores groups without useful variance', () => {
  const outliers = detectOutliers(
    {
      app: [
        { id: '1', name: 'a', cpuPct: 10, memPct: 20 },
        { id: '2', name: 'b', cpuPct: 10, memPct: 20 }
      ]
    },
    {
      outlierZScore: 1.5,
      outlierMinCpuPct: 40,
      outlierMinMemPct: 40
    }
  );

  assert.deepEqual(outliers, []);
});
