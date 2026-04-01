const test = require('node:test');
const assert = require('node:assert/strict');

const { findContainerByNameOrId } = require('../src/docker');

const containers = [
  {
    id: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    name: 'api',
    names: ['api', '/api'],
    image: 'my-org/api:1.0'
  },
  {
    id: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    name: 'worker-1',
    names: ['worker-1'],
    image: null
  }
];

test('findContainerByNameOrId matches by exact name and id prefix', () => {
  assert.equal(findContainerByNameOrId(containers, 'api').id, containers[0].id);
  assert.equal(findContainerByNameOrId(containers, 'aaaaaaaaaaaa').name, 'api');
});

test('findContainerByNameOrId supports partial name and null-safe image lookup', () => {
  assert.equal(findContainerByNameOrId(containers, 'worker').id, containers[1].id);
  assert.equal(findContainerByNameOrId(containers, 'my-org/api').id, containers[0].id);
  assert.equal(findContainerByNameOrId(containers, 'unknown'), undefined);
});
