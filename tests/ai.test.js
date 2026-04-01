const test = require('node:test');
const assert = require('node:assert/strict');

const { detectIntent, shouldSkipAI, summarizeWithAI } = require('../src/ai');

const ORIGINAL_ENV = { ...process.env };

test.afterEach(() => {
  for (const key of Object.keys(process.env)) {
    if (!(key in ORIGINAL_ENV)) {
      delete process.env[key];
    }
  }
  Object.assign(process.env, ORIGINAL_ENV);
});

test('detectIntent resolves core intents and remediation actions', () => {
  assert.deepEqual(detectIntent('who are you'), { intent: 'identity', target: null });
  assert.deepEqual(detectIntent('usage daily'), { intent: 'usage', target: 'daily' });
  assert.deepEqual(detectIntent('logs api'), { intent: 'logs', target: 'api' });
  assert.deepEqual(detectIntent('check group wordpress'), { intent: 'group_check', target: 'wordpress' });
  assert.deepEqual(detectIntent('what changed'), { intent: 'what_changed', target: null });
  assert.deepEqual(detectIntent('troubleshoot api'), { intent: 'troubleshoot', target: 'api' });
  assert.deepEqual(detectIntent('restart api'), {
    intent: 'remediation_request',
    action: 'restart',
    target: 'api'
  });
  assert.deepEqual(detectIntent('start worker'), {
    intent: 'remediation_request',
    action: 'start',
    target: 'worker'
  });
  assert.deepEqual(detectIntent('confirm ABC123'), { intent: 'remediation_confirm', target: 'abc123' });
  assert.deepEqual(detectIntent('cancel'), { intent: 'remediation_cancel', target: null });
});

test('detectIntent drops pronoun targets when they are not explicit container identifiers', () => {
  assert.deepEqual(detectIntent('logs it'), { intent: 'logs', target: null });
  assert.deepEqual(detectIntent('restart it'), {
    intent: 'remediation_request',
    action: 'restart',
    target: null
  });
  assert.deepEqual(detectIntent('check it'), { intent: 'container_check', target: null });
});

test('shouldSkipAI marks degraded containers correctly', () => {
  assert.equal(shouldSkipAI({ state: 'exited', health: 'none' }), true);
  assert.equal(shouldSkipAI({ state: 'running', health: 'unhealthy' }), true);
  assert.equal(shouldSkipAI({ state: 'running', health: 'healthy', restarting: true }), true);
  assert.equal(shouldSkipAI({ state: 'running', health: 'healthy', restarting: false }), false);
});

test('summarizeWithAI returns disabled when AI is turned off', async () => {
  const result = await summarizeWithAI({
    message: 'report',
    intent: 'full_report',
    facts: {},
    allowAI: false
  });

  assert.equal(result.called, false);
  assert.equal(result.reason, 'disabled');
});

test('summarizeWithAI returns missing_api_key when key is absent', async () => {
  delete process.env.OPENAI_API_KEY;

  const result = await summarizeWithAI({
    message: 'report',
    intent: 'full_report',
    facts: {},
    allowAI: true
  });

  assert.equal(result.called, false);
  assert.equal(result.reason, 'missing_api_key');
});
