const test = require('node:test');
const assert = require('node:assert/strict');

const { parseIncomingWebhook, getAllowedNumberSet, normalizePhone } = require('../src/whatsapp');

const ORIGINAL_ENV = { ...process.env };

function resetEnv() {
  for (const key of Object.keys(process.env)) {
    if (!(key in ORIGINAL_ENV)) {
      delete process.env[key];
    }
  }
  Object.assign(process.env, ORIGINAL_ENV);
}

test.afterEach(resetEnv);

function samplePayload(overrides = {}) {
  return {
    event_type: 'message_received',
    data: {
      fromMe: false,
      type: 'chat',
      from: '27821234567@c.us',
      body: 'full report'
    },
    ...overrides
  };
}

test('parseIncomingWebhook rejects unsupported events', () => {
  process.env.WA_ALLOWED_NUMBERS = '27821234567';
  const result = parseIncomingWebhook({ event_type: 'status_update' });
  assert.equal(result.shouldProcess, false);
  assert.equal(result.reason, 'event_not_supported');
});

test('parseIncomingWebhook enforces shared webhook secret when configured', () => {
  process.env.WA_ALLOWED_NUMBERS = '27821234567';
  process.env.WA_WEBHOOK_SECRET = 'abc123';

  const missing = parseIncomingWebhook(samplePayload());
  assert.equal(missing.reason, 'missing_webhook_secret');

  const invalid = parseIncomingWebhook(samplePayload({ webhook_secret: 'wrong' }));
  assert.equal(invalid.reason, 'invalid_webhook_secret');
});

test('parseIncomingWebhook accepts valid chat payload with allowlisted sender', () => {
  process.env.WA_ALLOWED_NUMBERS = '27821234567';
  process.env.WA_WEBHOOK_SECRET = 'abc123';

  const result = parseIncomingWebhook(samplePayload({ webhook_secret: 'abc123' }));
  assert.deepEqual(result, {
    shouldProcess: true,
    sender: '27821234567',
    message: 'full report'
  });
});

test('parseIncomingWebhook rejects non-whitelisted sender', () => {
  process.env.WA_ALLOWED_NUMBERS = '27829999999';
  const result = parseIncomingWebhook(samplePayload());
  assert.equal(result.shouldProcess, false);
  assert.equal(result.reason, 'not_whitelisted');
});

test('getAllowedNumberSet and normalizePhone sanitize expected values', () => {
  process.env.WA_ALLOWED_NUMBERS = '+27 82 123 4567,27831234567';
  const set = getAllowedNumberSet();

  assert.equal(set.has('27821234567'), true);
  assert.equal(set.has('27831234567'), true);
  assert.equal(normalizePhone('(082) 123-4567'), '0821234567');
});
