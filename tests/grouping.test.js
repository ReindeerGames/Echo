const test = require('node:test');
const assert = require('node:assert/strict');

const { applyGrouping, groupContainers, resolveGroup } = require('../src/grouping');

test('resolveGroup uses byName override first', () => {
  const group = resolveGroup(
    {
      name: 'API',
      image: 'my-org/api:latest',
      labels: {}
    },
    {
      grouping: {
        overrides: {
          byName: { api: 'edge-api' },
          byImage: { api: 'backend' }
        }
      }
    }
  );

  assert.equal(group, 'edge-api');
});

test('resolveGroup can use image overrides and compose labels', () => {
  const imageGroup = resolveGroup(
    {
      name: 'worker-1',
      image: 'my-registry/redis:7',
      labels: {}
    },
    {
      grouping: {
        overrides: {
          byName: {},
          byImage: { redis: 'cache' }
        }
      }
    }
  );
  assert.equal(imageGroup, 'cache');

  const composeGroup = resolveGroup(
    {
      name: 'service-1',
      image: 'custom/service:1',
      labels: { 'com.docker.compose.project': 'ProjectX' }
    },
    { grouping: { overrides: { byName: {}, byImage: {} } } }
  );
  assert.equal(composeGroup, 'projectx');
});

test('resolveGroup falls back to name prefix and ungrouped', () => {
  assert.equal(
    resolveGroup(
      { name: 'my-api-1', image: null, labels: {} },
      { grouping: { overrides: { byName: {}, byImage: {} } } }
    ),
    'my'
  );

  assert.equal(
    resolveGroup(
      { name: '', image: null, labels: {} },
      { grouping: { overrides: { byName: {}, byImage: {} } } }
    ),
    'ungrouped'
  );
});

test('applyGrouping and groupContainers assign and bucket groups', () => {
  const containers = [
    { id: '1', name: 'api-1', image: 'my/api:1', labels: {} },
    { id: '2', name: 'db-1', image: 'postgres:16', labels: {} }
  ];

  const grouped = applyGrouping(containers, {
    grouping: {
      overrides: {
        byName: {},
        byImage: { postgres: 'database' }
      }
    }
  });

  const buckets = groupContainers(grouped);
  assert.equal(Array.isArray(buckets.database), true);
  assert.equal(buckets.database.length, 1);
  assert.equal(grouped[1].group, 'database');
});
