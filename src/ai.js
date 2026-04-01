const fs = require('fs');
const path = require('path');
const OpenAI = require('openai');
const { loadEnabledSkills, pickRelevantSkills } = require('./skills');
const APP_ROOT = path.resolve(__dirname, '..');

let cachedClient = null;

function detectIntent(message) {
  const text = String(message || '').trim().toLowerCase();

  const confirmCode = matchFirst(
    text,
    /^\s*(?:confirm|approve|proceed|yes)\s+([a-z0-9]{4,12})\s*$/i
  );
  if (confirmCode) {
    return { intent: 'remediation_confirm', target: confirmCode };
  }
  if (/^\s*(?:confirm|approve|proceed|yes)\b/i.test(text)) {
    return { intent: 'remediation_confirm', target: null };
  }
  if (/^\s*(?:cancel|abort|stop)\b/i.test(text)) {
    return { intent: 'remediation_cancel', target: null };
  }

  if (/\b(help|who are you|what are you|what can you do|capabilities)\b/.test(text)) {
    return { intent: 'identity', target: null };
  }

  if (/\busage\b/.test(text)) {
    if (/\b(daily|today)\b/.test(text)) {
      return { intent: 'usage', target: 'daily' };
    }
    if (/\b(weekly|week)\b/.test(text)) {
      return { intent: 'usage', target: 'weekly' };
    }
    if (/\b(monthly|month)\b/.test(text)) {
      return { intent: 'usage', target: 'monthly' };
    }
    return { intent: 'usage', target: 'full' };
  }

  const logsTargetRaw = matchFirst(text, /\b(?:logs?|log)\s+(?:for\s+)?([a-z0-9._-]+)/i);
  const logsTarget = normalizeTargetCandidate(logsTargetRaw);
  if (logsTarget) {
    return { intent: 'logs', target: logsTarget };
  }
  if (/\b(?:logs?|log)\b/i.test(text)) {
    return { intent: 'logs', target: null };
  }

  const groupTarget =
    matchFirst(text, /\b(?:check\s+group|group|service\s+group)\s+([a-z0-9._-]+)/i) ||
    matchFirst(text, /\bgroup\s*:\s*([a-z0-9._-]+)/i);
  if (groupTarget) {
    return { intent: 'group_check', target: groupTarget };
  }
  if (/\b(check\s+group|group|service\s+group)\b/i.test(text)) {
    return { intent: 'group_check', target: null };
  }

  if (/\b(what\s+changed|what\'s\s+changed|delta|changes\s+since)\b/i.test(text)) {
    return { intent: 'what_changed', target: null };
  }

  const troubleshootTarget = normalizeTargetCandidate(
    matchFirst(text, /\b(?:troubleshoot|investigate|diagnose|debug|fix)\s+(?:container\s+)?([a-z0-9._-]+)/i) ||
      matchFirst(text, /\b(?:look\s+into|drill\s+into)\s+([a-z0-9._-]+)/i)
  );
  if (troubleshootTarget) {
    return { intent: 'troubleshoot', target: troubleshootTarget };
  }

  if (
    /\b(troubleshoot|investigate|diagnose|debug|root\s*cause|look\s+into|drill\s+into)\b/i.test(text) ||
    /\bfix\s+(it|this|that|one)\b/i.test(text)
  ) {
    return { intent: 'troubleshoot', target: null };
  }

  const restartTarget = normalizeTargetCandidate(
    matchFirst(text, /\b(?:restart|reboot|bounce)\s+(?:container\s+)?([a-z0-9._-]+)/i)
  );
  if (restartTarget) {
    return { intent: 'remediation_request', action: 'restart', target: restartTarget };
  }
  if (/\b(?:restart|reboot|bounce)\b/i.test(text)) {
    return { intent: 'remediation_request', action: 'restart', target: null };
  }

  const startTarget = normalizeTargetCandidate(
    matchFirst(text, /\b(?:start|run|bring\s+up)\s+(?:container\s+)?([a-z0-9._-]+)/i)
  );
  if (startTarget) {
    return { intent: 'remediation_request', action: 'start', target: startTarget };
  }
  if (/\b(?:start|run|bring\s+up)\b/i.test(text)) {
    return { intent: 'remediation_request', action: 'start', target: null };
  }

  if (/\b(priority|critical|urgent|sev1|p1)\b/i.test(text)) {
    return { intent: 'priority', target: null };
  }

  if (/\btop\b/i.test(text)) {
    const metric = matchFirst(text, /\b(cpu|memory|mem)\b/i) || 'combined';
    return { intent: 'top', target: metric };
  }

  const containerTarget =
    normalizeTargetCandidate(matchFirst(text, /\b(?:check|inspect|status)\s+([a-z0-9._-]+)/i)) ||
    normalizeTargetCandidate(matchFirst(text, /^([a-z0-9._-]+)\s+status$/i));
  if (containerTarget) {
    return { intent: 'container_check', target: containerTarget };
  }
  if (/\b(check|inspect|status)\b/i.test(text)) {
    return { intent: 'container_check', target: null };
  }

  if (/\b(full\s*report|report|estate|overview|summary)\b/i.test(text)) {
    return { intent: 'full_report', target: null };
  }

  return { intent: 'full_report', target: null };
}

function shouldSkipAI(container) {
  if (!container) {
    return false;
  }

  const state = String(container.state || '').toLowerCase();
  const health = String(container.health || '').toLowerCase();

  return (
    state === 'restarting' ||
    state === 'exited' ||
    state === 'dead' ||
    state === 'paused' ||
    health === 'unhealthy' ||
    container.restarting
  );
}

async function summarizeWithAI(input) {
  const { message, intent, facts, filteredLogs, model, allowAI, context } = input;

  if (!allowAI) {
    return {
      called: false,
      text: null,
      reason: 'disabled'
    };
  }

  const client = getClient();
  if (!client) {
    return {
      called: false,
      text: null,
      reason: 'missing_api_key'
    };
  }

  const selectedModel = model || process.env.OPENAI_MODEL || 'gpt-4o-mini';
  const identityPrompt = readIdentityPrompt();
  const skills = loadEnabledSkills(APP_ROOT);
  const selectedSkills = pickRelevantSkills(message, skills, 2);
  const skillsBlock = selectedSkills
    .map((skill) => `Skill: ${skill.path}\n${trimSkill(skill.content)}`)
    .join('\n\n');

  const userPayload = {
    intent,
    user_message: message,
    skills_in_play: selectedSkills.map((skill) => skill.path),
    capabilities_and_environment: context || null,
    facts,
    filtered_log_signals: filteredLogs || null,
    output_style:
      'WhatsApp-ready plain text. Sound natural and useful, not rigid. Start with outcome first when user asked for action. Use short paragraphs or bullets only when needed. Avoid fixed headings, avoid filler, avoid emojis.'
  };

  try {
    const response = await client.responses.create({
      model: selectedModel,
      max_output_tokens: 220,
      input: [
        {
          role: 'system',
          content: `${identityPrompt}\n\n${skillsBlock}`.trim()
        },
        {
          role: 'user',
          content: JSON.stringify(userPayload)
        }
      ]
    });

    const text = extractOutputText(response);
    const normalizedText = normalizeModelText(text);
    const usage = extractUsage(response);

    return {
      called: true,
      text: normalizedText,
      usage,
      model: response && response.model ? response.model : selectedModel,
      responseId: response && response.id ? response.id : null,
      status: response && response.status ? response.status : 'completed',
      error: null
    };
  } catch (error) {
    console.error('[ai] summary failed:', error.message);
    return {
      called: true,
      text: null,
      usage: null,
      model: selectedModel,
      responseId: null,
      status: 'error',
      error: error.message || 'ai_error'
    };
  }
}

function getClient() {
  if (!process.env.OPENAI_API_KEY) {
    return null;
  }

  if (!cachedClient) {
    cachedClient = new OpenAI({ apiKey: process.env.OPENAI_API_KEY });
  }

  return cachedClient;
}

function readIdentityPrompt() {
  const filePath = path.join(APP_ROOT, 'prompts', 'identity.md');
  try {
    return fs.readFileSync(filePath, 'utf8');
  } catch (error) {
    console.error('[ai] Failed to read identity prompt:', error.message);
    return 'You are Echo, an SRE assistant. Keep responses concise and actionable.';
  }
}

function trimSkill(content) {
  const lines = String(content || '')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .slice(0, 12);
  return lines.join('\n');
}

function matchFirst(text, regex) {
  const match = String(text || '').match(regex);
  return match && match[1] ? String(match[1]).toLowerCase() : null;
}

function extractOutputText(response) {
  if (!response) {
    return '';
  }

  if (typeof response.output_text === 'string' && response.output_text.trim()) {
    return response.output_text.trim();
  }

  const output = Array.isArray(response.output) ? response.output : [];
  for (const item of output) {
    const content = Array.isArray(item.content) ? item.content : [];
    for (const part of content) {
      if (part.type === 'output_text' && typeof part.text === 'string' && part.text.trim()) {
        return part.text.trim();
      }
    }
  }

  return '';
}

function extractUsage(response) {
  const usage = response && response.usage ? response.usage : {};

  const inputTokens = toInt(
    usage.input_tokens !== undefined ? usage.input_tokens : usage.prompt_tokens
  );
  const cachedInputTokens = toInt(
    usage.input_tokens_details && usage.input_tokens_details.cached_tokens !== undefined
      ? usage.input_tokens_details.cached_tokens
      : usage.prompt_tokens_details && usage.prompt_tokens_details.cached_tokens !== undefined
        ? usage.prompt_tokens_details.cached_tokens
        : 0
  );
  const outputTokens = toInt(
    usage.output_tokens !== undefined ? usage.output_tokens : usage.completion_tokens
  );
  const reasoningTokens = toInt(
    usage.output_tokens_details && usage.output_tokens_details.reasoning_tokens !== undefined
      ? usage.output_tokens_details.reasoning_tokens
      : usage.completion_tokens_details && usage.completion_tokens_details.reasoning_tokens !== undefined
        ? usage.completion_tokens_details.reasoning_tokens
        : 0
  );
  const totalTokens = toInt(
    usage.total_tokens !== undefined ? usage.total_tokens : inputTokens + outputTokens
  );

  return {
    inputTokens,
    cachedInputTokens,
    outputTokens,
    reasoningTokens,
    totalTokens,
    raw: usage
  };
}

function normalizeModelText(text) {
  const value = String(text || '')
    .replace(/\r/g, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
  return value || null;
}

function normalizeTargetCandidate(value) {
  const candidate = String(value || '').trim().toLowerCase();
  if (!candidate) {
    return null;
  }

  if (/^(it|this|that|one|please|pls|now|thanks|thank-you|thx|me|container|service)$/.test(candidate)) {
    return null;
  }

  return candidate;
}

function toInt(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n < 0) {
    return 0;
  }
  return Math.floor(n);
}

module.exports = {
  detectIntent,
  shouldSkipAI,
  summarizeWithAI
};
