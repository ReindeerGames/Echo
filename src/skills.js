const fs = require('fs');
const path = require('path');
const APP_ROOT = path.resolve(__dirname, '..');

function loadEnabledSkills(baseDir = APP_ROOT) {
  const registryPath = path.join(baseDir, 'skills', 'registry.json');
  let registry = { enabled: [] };

  try {
    registry = JSON.parse(fs.readFileSync(registryPath, 'utf8'));
  } catch (error) {
    console.error('[skills] Failed to read registry:', error.message);
  }

  const enabled = Array.isArray(registry.enabled) ? registry.enabled : [];
  return enabled
    .map((relativePath) => {
      const abs = path.join(baseDir, 'skills', relativePath);
      try {
        return {
          path: relativePath,
          content: fs.readFileSync(abs, 'utf8')
        };
      } catch (error) {
        console.error(`[skills] Failed to read ${relativePath}:`, error.message);
        return null;
      }
    })
    .filter(Boolean);
}

function pickRelevantSkills(message, skills, max = 2) {
  const text = String(message || '').toLowerCase();
  const scored = skills.map((skill) => {
    const stem = path.basename(skill.path, '.md').replace(/[-_]/g, ' ');
    const tokens = stem.split(/\s+/).filter(Boolean);
    let score = 0;

    for (const token of tokens) {
      if (token.length > 2 && text.includes(token)) {
        score += 1;
      }
    }

    return { ...skill, score };
  });

  return scored
    .sort((a, b) => b.score - a.score)
    .filter((item, index) => item.score > 0 || index === 0)
    .slice(0, max);
}

function normalizeQuery(message) {
  return String(message || '')
    .toLowerCase()
    .replace(/[^a-z0-9\s]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function maybeCreateSkillDraft(input) {
  const { state, message, intent, target, features } = input;

  if (!features || !features.skillDrafting) {
    return null;
  }

  const normalized = normalizeQuery(message);
  if (!normalized) {
    return null;
  }

  const seenCount = state.countSimilarQueries(normalized, 1440);
  if (seenCount < 3) {
    return null;
  }

  const fingerprint = `${intent}:${target || '*'}:${normalized}`;
  if (state.hasPendingSkillProposal(fingerprint)) {
    return null;
  }

  const slug = slugify(`${intent}-${target || 'general'}`);
  const filename = `${new Date().toISOString().slice(0, 10)}-${slug}.md`;
  const relativePath = path.join('skills', 'drafts', filename);
  const absolutePath = path.join(APP_ROOT, relativePath);

  const title = `Draft Skill: ${intent}${target ? ` (${target})` : ''}`;
  const body = [
    `# ${title}`,
    '',
    '## Trigger',
    `Repeated query detected: "${normalized}"`,
    '',
    '## Workflow',
    '1. Collect deterministic container status and health.',
    '2. Pull resource metrics and compare against group baseline.',
    '3. If needed, fetch filtered logs (dedupe + truncate + score).',
    '4. Return concise issue, evidence, and recommendation.',
    '',
    '## Notes',
    '- Pending manual approval before activation in skills/custom/.'
  ].join('\n');

  fs.writeFileSync(absolutePath, `${body}\n`, 'utf8');

  const proposalSaved = state.saveSkillProposal({
    title,
    fingerprint,
    draftPath: relativePath,
    reason: 'Repeated investigation workflow detected in recent queries.',
    examples: [message]
  });

  if (!proposalSaved) {
    return null;
  }

  return relativePath;
}

function slugify(text) {
  return String(text || 'skill')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48);
}

module.exports = {
  loadEnabledSkills,
  pickRelevantSkills,
  normalizeQuery,
  maybeCreateSkillDraft
};
