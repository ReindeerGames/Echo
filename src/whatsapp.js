const crypto = require('crypto');

function parseIncomingWebhook(payload) {
  if (!payload || payload.event_type !== 'message_received') {
    return { shouldProcess: false, reason: 'event_not_supported' };
  }

  const expectedWebhookSecret = sanitizeEnv(process.env.WA_WEBHOOK_SECRET);
  if (expectedWebhookSecret) {
    const providedWebhookSecret = sanitizeEnv(payload.webhook_secret);
    if (!providedWebhookSecret) {
      return { shouldProcess: false, reason: 'missing_webhook_secret' };
    }
    if (!constantTimeEqual(providedWebhookSecret, expectedWebhookSecret)) {
      return { shouldProcess: false, reason: 'invalid_webhook_secret' };
    }
  }

  const data = payload.data || {};
  if (data.fromMe === true) {
    return { shouldProcess: false, reason: 'from_me' };
  }

  if (data.type !== 'chat') {
    return { shouldProcess: false, reason: 'type_not_chat' };
  }

  const sender = normalizePhone((data.from || '').split('@')[0]);
  const message = String(data.body || '').trim();

  if (!sender || !message) {
    return { shouldProcess: false, reason: 'invalid_payload' };
  }

  const allowed = getAllowedNumberSet();
  if (!allowed.has(sender)) {
    return { shouldProcess: false, reason: 'not_whitelisted', sender };
  }

  return {
    shouldProcess: true,
    sender,
    message
  };
}

async function sendWhatsAppMessage(to, body) {
  const baseUrl = normalizeUrlBase(process.env.WA_BASE_URL);
  const instance = sanitizeEnv(process.env.WA_INSTANCE);
  const token = sanitizeEnv(process.env.WA_TOKEN);

  if (!baseUrl || !instance || !token) {
    console.error('[whatsapp] Missing WA_BASE_URL/WA_INSTANCE/WA_TOKEN configuration.');
    return false;
  }

  const url = `${baseUrl}/${instance}/messages/chat`;
  const timeoutMs = toInt(process.env.WA_SEND_TIMEOUT_MS, 30000);
  const payload = new URLSearchParams();
  payload.set('token', token);
  payload.set('to', normalizePhone(to));
  payload.set('body', String(body || '').trim());

  try {
    const response = await fetchWithTimeout(
      url,
      {
        method: 'POST',
        headers: {
          'content-type': 'application/x-www-form-urlencoded',
          accept: 'application/json'
        },
        body: payload
      },
      timeoutMs
    );

    const bodyText = await response.text();
    const data = tryParseJson(bodyText);

    if (!response.ok) {
      console.error(
        `[whatsapp] Failed to send message: http_status=${response.status} body=${truncateForLog(bodyText, 240)}`
      );
      return false;
    }

    // GoChat can respond with "Sending request..." before final delivery ack.
    if (data && typeof data === 'object') {
      const sent = String(data.sent || '').toLowerCase();
      const messageText = String(data.message || '').toLowerCase();
      const accepted = sent === 'true' || sent === '1' || messageText.includes('ok') || messageText.includes('sending request');
      if (!accepted) {
        console.log(`[whatsapp] Send accepted with non-standard response: ${truncateForLog(bodyText, 240)}`);
      }
    }

    return true;
  } catch (error) {
    if (isTimeoutError(error)) {
      console.warn(
        `[whatsapp] Send response timed out after ${timeoutMs}ms. Message may still be delivered by GoChat.`
      );
      return true;
    }

    console.error('[whatsapp] Failed to send message:', getFetchError(error));
    return false;
  }
}

async function checkWhatsAppStatus(options = {}) {
  const baseUrl = normalizeUrlBase(process.env.WA_BASE_URL);
  const instance = sanitizeEnv(process.env.WA_INSTANCE);
  const token = sanitizeEnv(process.env.WA_TOKEN);

  if (!baseUrl || !instance || !token) {
    return { ok: false, reason: 'missing_config' };
  }

  const url = `${baseUrl}/${instance}/instance/status?token=${encodeURIComponent(token)}`;
  const retries = toInt(options.retries, toInt(process.env.WA_STATUS_RETRIES, 3));
  const timeoutMs = toInt(options.timeoutMs, toInt(process.env.WA_STATUS_TIMEOUT_MS, 10000));
  const retryDelayMs = toInt(options.retryDelayMs, toInt(process.env.WA_STATUS_RETRY_DELAY_MS, 1200));

  let lastError = 'unknown_error';
  for (let attempt = 1; attempt <= retries; attempt += 1) {
    try {
      const response = await fetchWithTimeout(
        url,
        {
          method: 'GET',
          headers: {
            Accept: 'application/json'
          }
        },
        timeoutMs
      );
      const bodyText = await response.text();
      const data = tryParseJson(bodyText);

      if (!response.ok) {
        throw new Error(
          `http_status=${response.status} body=${truncateForLog(bodyText, 240)}`
        );
      }

      const account =
        data &&
        data.status &&
        data.status.accountStatus
          ? data.status.accountStatus
          : {};

      return {
        ok: true,
        status: account.status || 'unknown',
        substatus: account.substatus || 'unknown',
        attempts: attempt
      };
    } catch (error) {
      lastError = getFetchError(error);
      if (attempt < retries) {
        await sleep(retryDelayMs);
      }
    }
  }

  return {
    ok: false,
    reason: `after ${retries} attempt(s): ${lastError}`,
    attempts: retries
  };
}

function getAllowedNumberSet() {
  const raw = process.env.WA_ALLOWED_NUMBERS || '';
  const values = raw
    .split(',')
    .map((item) => normalizePhone(item))
    .filter(Boolean);
  return new Set(values);
}

function normalizePhone(value) {
  return String(value || '').replace(/[^\d]/g, '');
}

function sanitizeEnv(value) {
  return String(value || '')
    .trim()
    .replace(/^['"]+|['"]+$/g, '');
}

function normalizeUrlBase(value) {
  return sanitizeEnv(value).replace(/\/+$/, '');
}

function toInt(value, fallback) {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) {
    return fallback;
  }
  return Math.floor(n);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function fetchWithTimeout(url, options, timeoutMs) {
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), timeoutMs);

  try {
    return await fetch(url, {
      ...options,
      signal: controller.signal
    });
  } finally {
    clearTimeout(timeoutId);
  }
}

function getFetchError(error) {
  if (!error) {
    return 'unknown_error';
  }

  const name = error.name ? `name=${error.name}` : '';
  const code =
    error.code
      ? `code=${error.code}`
      : error.cause && error.cause.code
        ? `code=${error.cause.code}`
        : '';

  return `${error.message || 'fetch_error'} ${name} ${code}`.trim();
}

function isTimeoutError(error) {
  if (!error) {
    return false;
  }

  if (error.name === 'AbortError') {
    return true;
  }

  const message = String(error.message || '').toLowerCase();
  const code = String(error.code || (error.cause && error.cause.code) || '').toLowerCase();
  return message.includes('timeout') || code.includes('timedout') || code === 'etimedout';
}

function tryParseJson(text) {
  try {
    return JSON.parse(String(text || ''));
  } catch (_error) {
    return null;
  }
}

function truncateForLog(text, maxChars = 240) {
  const value = String(text || '').replace(/\s+/g, ' ').trim();
  if (value.length <= maxChars) {
    return value;
  }
  return `${value.slice(0, Math.max(0, maxChars - 3))}...`;
}

function constantTimeEqual(a, b) {
  const left = Buffer.from(String(a || ''), 'utf8');
  const right = Buffer.from(String(b || ''), 'utf8');

  if (left.length !== right.length) {
    return false;
  }

  return crypto.timingSafeEqual(left, right);
}

module.exports = {
  parseIncomingWebhook,
  sendWhatsAppMessage,
  checkWhatsAppStatus,
  getAllowedNumberSet,
  normalizePhone
};
