const fs = require('fs');
const path = require('path');
const Database = require('better-sqlite3');
const APP_ROOT = path.resolve(__dirname, '..');

function initState(dbPath = path.join(APP_ROOT, 'data', 'echo.db')) {
  const dir = path.dirname(dbPath);
  fs.mkdirSync(dir, { recursive: true });

  const db = new Database(dbPath);
  db.pragma('journal_mode = WAL');

  db.exec(`
    CREATE TABLE IF NOT EXISTS issues (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      issue_key TEXT,
      container_id TEXT,
      container_name TEXT,
      group_name TEXT,
      type TEXT NOT NULL,
      severity TEXT NOT NULL,
      evidence TEXT NOT NULL,
      recommendation TEXT NOT NULL,
      status TEXT DEFAULT 'open',
      created_at TEXT DEFAULT CURRENT_TIMESTAMP
    );

    CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at);
    CREATE INDEX IF NOT EXISTS idx_issues_container ON issues(container_id);

    CREATE TABLE IF NOT EXISTS snapshots (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      ts TEXT NOT NULL,
      container_id TEXT NOT NULL,
      container_name TEXT NOT NULL,
      image TEXT,
      group_name TEXT,
      state TEXT,
      health TEXT,
      cpu_pct REAL,
      mem_bytes INTEGER,
      mem_limit INTEGER,
      mem_pct REAL,
      raw_json TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_snapshots_ts ON snapshots(ts);
    CREATE INDEX IF NOT EXISTS idx_snapshots_container ON snapshots(container_id);

    CREATE TABLE IF NOT EXISTS anomalies (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      ts TEXT NOT NULL,
      container_id TEXT,
      container_name TEXT,
      group_name TEXT,
      metric TEXT,
      value REAL,
      baseline REAL,
      z_score REAL,
      note TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_anomalies_ts ON anomalies(ts);

    CREATE TABLE IF NOT EXISTS recent_queries (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      ts TEXT DEFAULT CURRENT_TIMESTAMP,
      sender TEXT,
      intent TEXT,
      target TEXT,
      message TEXT,
      normalized_message TEXT,
      response TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_recent_queries_ts ON recent_queries(ts);
    CREATE INDEX IF NOT EXISTS idx_recent_queries_norm ON recent_queries(normalized_message);

    CREATE TABLE IF NOT EXISTS skill_proposals (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      ts TEXT DEFAULT CURRENT_TIMESTAMP,
      title TEXT NOT NULL,
      fingerprint TEXT NOT NULL,
      draft_path TEXT NOT NULL,
      reason TEXT,
      examples TEXT,
      status TEXT DEFAULT 'pending'
    );

    CREATE UNIQUE INDEX IF NOT EXISTS idx_skill_proposals_fingerprint ON skill_proposals(fingerprint);

    CREATE TABLE IF NOT EXISTS ai_pricing (
      model TEXT PRIMARY KEY,
      input_per_million REAL NOT NULL,
      cached_input_per_million REAL NOT NULL,
      output_per_million REAL NOT NULL,
      currency TEXT NOT NULL DEFAULT 'USD',
      source_url TEXT,
      updated_at TEXT DEFAULT CURRENT_TIMESTAMP
    );

    CREATE TABLE IF NOT EXISTS ai_usage_events (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      ts TEXT NOT NULL,
      sender TEXT,
      intent TEXT,
      model TEXT,
      response_id TEXT,
      request_id TEXT,
      status TEXT NOT NULL,
      error TEXT,
      input_tokens INTEGER DEFAULT 0,
      cached_input_tokens INTEGER DEFAULT 0,
      output_tokens INTEGER DEFAULT 0,
      reasoning_tokens INTEGER DEFAULT 0,
      total_tokens INTEGER DEFAULT 0,
      input_cost_usd REAL DEFAULT 0,
      cached_input_cost_usd REAL DEFAULT 0,
      output_cost_usd REAL DEFAULT 0,
      total_cost_usd REAL DEFAULT 0,
      raw_usage_json TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_ai_usage_events_ts ON ai_usage_events(ts);
    CREATE INDEX IF NOT EXISTS idx_ai_usage_events_model ON ai_usage_events(model);
    CREATE INDEX IF NOT EXISTS idx_ai_usage_events_intent ON ai_usage_events(intent);
  `);

  const insertSnapshotStmt = db.prepare(`
    INSERT INTO snapshots (
      ts, container_id, container_name, image, group_name, state, health,
      cpu_pct, mem_bytes, mem_limit, mem_pct, raw_json
    ) VALUES (
      @ts, @container_id, @container_name, @image, @group_name, @state, @health,
      @cpu_pct, @mem_bytes, @mem_limit, @mem_pct, @raw_json
    )
  `);

  const insertIssueStmt = db.prepare(`
    INSERT INTO issues (
      issue_key, container_id, container_name, group_name, type, severity, evidence, recommendation
    ) VALUES (
      @issue_key, @container_id, @container_name, @group_name, @type, @severity, @evidence, @recommendation
    )
  `);

  const insertAnomalyStmt = db.prepare(`
    INSERT INTO anomalies (
      ts, container_id, container_name, group_name, metric, value, baseline, z_score, note
    ) VALUES (
      @ts, @container_id, @container_name, @group_name, @metric, @value, @baseline, @z_score, @note
    )
  `);

  const insertRecentQueryStmt = db.prepare(`
    INSERT INTO recent_queries (
      sender, intent, target, message, normalized_message, response
    ) VALUES (
      @sender, @intent, @target, @message, @normalized_message, @response
    )
  `);

  const insertSkillProposalStmt = db.prepare(`
    INSERT INTO skill_proposals (
      title, fingerprint, draft_path, reason, examples, status
    ) VALUES (
      @title, @fingerprint, @draft_path, @reason, @examples, 'pending'
    )
  `);

  const upsertAIPricingStmt = db.prepare(`
    INSERT INTO ai_pricing (
      model, input_per_million, cached_input_per_million, output_per_million, currency, source_url, updated_at
    ) VALUES (
      @model, @input_per_million, @cached_input_per_million, @output_per_million, @currency, @source_url, CURRENT_TIMESTAMP
    )
    ON CONFLICT(model) DO UPDATE SET
      input_per_million = excluded.input_per_million,
      cached_input_per_million = excluded.cached_input_per_million,
      output_per_million = excluded.output_per_million,
      currency = excluded.currency,
      source_url = excluded.source_url,
      updated_at = CURRENT_TIMESTAMP
  `);

  const getAIPricingByModelStmt = db.prepare(`
    SELECT *
    FROM ai_pricing
    WHERE model = ?
    LIMIT 1
  `);

  const insertAIUsageEventStmt = db.prepare(`
    INSERT INTO ai_usage_events (
      ts, sender, intent, model, response_id, request_id, status, error,
      input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, total_tokens,
      input_cost_usd, cached_input_cost_usd, output_cost_usd, total_cost_usd, raw_usage_json
    ) VALUES (
      @ts, @sender, @intent, @model, @response_id, @request_id, @status, @error,
      @input_tokens, @cached_input_tokens, @output_tokens, @reasoning_tokens, @total_tokens,
      @input_cost_usd, @cached_input_cost_usd, @output_cost_usd, @total_cost_usd, @raw_usage_json
    )
  `);

  const saveSnapshotsTx = db.transaction((snapshots) => {
    for (const snapshot of snapshots) {
      insertSnapshotStmt.run(snapshot);
    }
  });

  const saveIssuesTx = db.transaction((issues) => {
    for (const issue of issues) {
      insertIssueStmt.run(issue);
    }
  });

  const saveAnomaliesTx = db.transaction((anomalies) => {
    for (const anomaly of anomalies) {
      insertAnomalyStmt.run(anomaly);
    }
  });

  function saveSnapshots(snapshots = [], ts = new Date().toISOString()) {
    if (!snapshots.length) {
      return;
    }

    const rows = snapshots.map((item) => ({
      ts,
      container_id: item.id,
      container_name: item.name,
      image: item.image || null,
      group_name: item.group || 'ungrouped',
      state: item.state || 'unknown',
      health: item.health || 'none',
      cpu_pct: isFiniteNumber(item.cpuPct) ? item.cpuPct : null,
      mem_bytes: isFiniteNumber(item.memBytes) ? item.memBytes : null,
      mem_limit: isFiniteNumber(item.memLimit) ? item.memLimit : null,
      mem_pct: isFiniteNumber(item.memPct) ? item.memPct : null,
      raw_json: JSON.stringify(item)
    }));

    saveSnapshotsTx(rows);
  }

  function saveIssues(issues = []) {
    if (!issues.length) {
      return;
    }

    const rows = issues.map((item) => ({
      issue_key: item.issueKey || `${item.type}:${item.containerId || item.containerName || 'global'}`,
      container_id: item.containerId || null,
      container_name: item.containerName || null,
      group_name: item.group || null,
      type: item.type,
      severity: item.severity,
      evidence: item.evidence,
      recommendation: item.recommendation
    }));

    saveIssuesTx(rows);
  }

  function saveAnomalies(anomalies = [], ts = new Date().toISOString()) {
    if (!anomalies.length) {
      return;
    }

    const rows = anomalies.map((item) => ({
      ts,
      container_id: item.containerId || null,
      container_name: item.containerName || null,
      group_name: item.group || null,
      metric: item.metric,
      value: item.value,
      baseline: item.baseline,
      z_score: item.zScore,
      note: item.note || ''
    }));

    saveAnomaliesTx(rows);
  }

  function saveRecentQuery(query) {
    insertRecentQueryStmt.run({
      sender: query.sender || null,
      intent: query.intent || null,
      target: query.target || null,
      message: query.message || '',
      normalized_message: query.normalizedMessage || '',
      response: query.response || ''
    });
  }

  function getRecentQueries(limit = 50) {
    return db.prepare(`
      SELECT *
      FROM recent_queries
      ORDER BY ts DESC, id DESC
      LIMIT ?
    `).all(limit);
  }

  function countSimilarQueries(normalizedMessage, lookbackMinutes = 1440) {
    if (!normalizedMessage) {
      return 0;
    }

    const row = db.prepare(`
      SELECT COUNT(*) AS count
      FROM recent_queries
      WHERE normalized_message = ?
      AND ts >= datetime('now', ?)
    `).get(normalizedMessage, `-${lookbackMinutes} minutes`);

    return row ? row.count : 0;
  }

  function hasPendingSkillProposal(fingerprint) {
    const row = db.prepare(`
      SELECT id
      FROM skill_proposals
      WHERE fingerprint = ? AND status = 'pending'
      LIMIT 1
    `).get(fingerprint);

    return Boolean(row);
  }

  function saveSkillProposal(proposal) {
    try {
      insertSkillProposalStmt.run({
        title: proposal.title,
        fingerprint: proposal.fingerprint,
        draft_path: proposal.draftPath,
        reason: proposal.reason || '',
        examples: JSON.stringify(proposal.examples || [])
      });
      return true;
    } catch (error) {
      if (String(error.message || '').includes('UNIQUE')) {
        return false;
      }
      throw error;
    }
  }

  function saveAIPricingRates(rates = []) {
    for (const item of rates) {
      upsertAIPricingStmt.run({
        model: item.model,
        input_per_million: toNumber(item.inputPerMillion, 0),
        cached_input_per_million: toNumber(item.cachedInputPerMillion, 0),
        output_per_million: toNumber(item.outputPerMillion, 0),
        currency: item.currency || 'USD',
        source_url: item.sourceUrl || null
      });
    }
  }

  function seedDefaultAIPricing() {
    const defaultRates = [
      {
        model: 'gpt-4o-mini',
        inputPerMillion: toNumber(process.env.AI_RATE_GPT4O_MINI_INPUT_PER_M, 0.15),
        cachedInputPerMillion: toNumber(process.env.AI_RATE_GPT4O_MINI_CACHED_INPUT_PER_M, 0.075),
        outputPerMillion: toNumber(process.env.AI_RATE_GPT4O_MINI_OUTPUT_PER_M, 0.6),
        currency: 'USD',
        sourceUrl: 'https://platform.openai.com/pricing'
      }
    ];

    saveAIPricingRates(defaultRates);
  }

  function resolveAIPricing(model) {
    const normalizedModel = String(model || '').trim();
    if (!normalizedModel) {
      return getAIPricingByModelStmt.get('gpt-4o-mini') || null;
    }

    const exact = getAIPricingByModelStmt.get(normalizedModel);
    if (exact) {
      return exact;
    }

    if (normalizedModel.startsWith('gpt-4o-mini')) {
      return getAIPricingByModelStmt.get('gpt-4o-mini') || null;
    }

    return null;
  }

  function saveAIUsageEvent(event = {}) {
    const usage = event.usage || {};
    const inputTokens = toInt(usage.inputTokens);
    const cachedInputTokens = Math.min(toInt(usage.cachedInputTokens), inputTokens);
    const outputTokens = toInt(usage.outputTokens);
    const reasoningTokens = toInt(usage.reasoningTokens);
    const totalTokens = toInt(usage.totalTokens || inputTokens + outputTokens);
    const uncachedInputTokens = Math.max(0, inputTokens - cachedInputTokens);

    const pricing = resolveAIPricing(event.model);
    const inputRate = pricing ? toNumber(pricing.input_per_million, 0) : 0;
    const cachedInputRate = pricing ? toNumber(pricing.cached_input_per_million, 0) : 0;
    const outputRate = pricing ? toNumber(pricing.output_per_million, 0) : 0;

    const inputCostUsd = roundUsd((uncachedInputTokens / 1000000) * inputRate);
    const cachedInputCostUsd = roundUsd((cachedInputTokens / 1000000) * cachedInputRate);
    const outputCostUsd = roundUsd((outputTokens / 1000000) * outputRate);
    const totalCostUsd = roundUsd(inputCostUsd + cachedInputCostUsd + outputCostUsd);

    insertAIUsageEventStmt.run({
      ts: event.ts || new Date().toISOString(),
      sender: event.sender || null,
      intent: event.intent || null,
      model: event.model || null,
      response_id: event.responseId || null,
      request_id: event.requestId || null,
      status: event.status || 'ok',
      error: event.error || null,
      input_tokens: inputTokens,
      cached_input_tokens: cachedInputTokens,
      output_tokens: outputTokens,
      reasoning_tokens: reasoningTokens,
      total_tokens: totalTokens,
      input_cost_usd: inputCostUsd,
      cached_input_cost_usd: cachedInputCostUsd,
      output_cost_usd: outputCostUsd,
      total_cost_usd: totalCostUsd,
      raw_usage_json: JSON.stringify(usage.raw || usage || {})
    });

    return {
      inputTokens,
      cachedInputTokens,
      outputTokens,
      reasoningTokens,
      totalTokens,
      inputCostUsd,
      cachedInputCostUsd,
      outputCostUsd,
      totalCostUsd
    };
  }

  function getAIUsageAggregate(sinceTs = null) {
    const row = sinceTs
      ? db.prepare(`
          SELECT
            COUNT(*) AS calls,
            SUM(input_tokens) AS input_tokens,
            SUM(cached_input_tokens) AS cached_input_tokens,
            SUM(output_tokens) AS output_tokens,
            SUM(reasoning_tokens) AS reasoning_tokens,
            SUM(total_tokens) AS total_tokens,
            SUM(input_cost_usd) AS input_cost_usd,
            SUM(cached_input_cost_usd) AS cached_input_cost_usd,
            SUM(output_cost_usd) AS output_cost_usd,
            SUM(total_cost_usd) AS total_cost_usd
          FROM ai_usage_events
          WHERE ts >= ?
        `).get(sinceTs)
      : db.prepare(`
          SELECT
            COUNT(*) AS calls,
            SUM(input_tokens) AS input_tokens,
            SUM(cached_input_tokens) AS cached_input_tokens,
            SUM(output_tokens) AS output_tokens,
            SUM(reasoning_tokens) AS reasoning_tokens,
            SUM(total_tokens) AS total_tokens,
            SUM(input_cost_usd) AS input_cost_usd,
            SUM(cached_input_cost_usd) AS cached_input_cost_usd,
            SUM(output_cost_usd) AS output_cost_usd,
            SUM(total_cost_usd) AS total_cost_usd
          FROM ai_usage_events
        `).get();

    return {
      calls: toInt(row && row.calls),
      inputTokens: toInt(row && row.input_tokens),
      cachedInputTokens: toInt(row && row.cached_input_tokens),
      outputTokens: toInt(row && row.output_tokens),
      reasoningTokens: toInt(row && row.reasoning_tokens),
      totalTokens: toInt(row && row.total_tokens),
      inputCostUsd: roundUsd(toNumber(row && row.input_cost_usd, 0)),
      cachedInputCostUsd: roundUsd(toNumber(row && row.cached_input_cost_usd, 0)),
      outputCostUsd: roundUsd(toNumber(row && row.output_cost_usd, 0)),
      totalCostUsd: roundUsd(toNumber(row && row.total_cost_usd, 0))
    };
  }

  function getAIUsageSummary() {
    const all = getAIUsageAggregate(null);
    const daily = getAIUsageAggregate(lookbackIso(1));
    const weekly = getAIUsageAggregate(lookbackIso(7));
    const monthly = getAIUsageAggregate(lookbackIso(30));
    const model = process.env.OPENAI_MODEL || 'gpt-4o-mini';
    const pricing = resolveAIPricing(model);

    return {
      all,
      daily,
      weekly,
      monthly,
      model,
      pricing: pricing
        ? {
            inputPerMillion: toNumber(pricing.input_per_million, 0),
            cachedInputPerMillion: toNumber(pricing.cached_input_per_million, 0),
            outputPerMillion: toNumber(pricing.output_per_million, 0),
            currency: pricing.currency || 'USD',
            sourceUrl: pricing.source_url || null
          }
        : null
    };
  }

  function getLatestSnapshotMap() {
    const rows = db.prepare(`
      SELECT *
      FROM (
        SELECT s.*, ROW_NUMBER() OVER (PARTITION BY s.container_id ORDER BY s.ts DESC, s.id DESC) AS rn
        FROM snapshots s
      )
      WHERE rn = 1
    `).all();

    const map = new Map();
    for (const row of rows) {
      map.set(row.container_id, row);
      map.set((row.container_name || '').toLowerCase(), row);
    }
    return map;
  }

  function getPreviousSnapshotMap() {
    const rows = db.prepare(`
      SELECT *
      FROM (
        SELECT s.*, ROW_NUMBER() OVER (PARTITION BY s.container_id ORDER BY s.ts DESC, s.id DESC) AS rn
        FROM snapshots s
      )
      WHERE rn = 2
    `).all();

    const map = new Map();
    for (const row of rows) {
      map.set(row.container_id, row);
      map.set((row.container_name || '').toLowerCase(), row);
    }
    return map;
  }

  function getRecentIssues(limit = 25, severities = []) {
    if (!severities.length) {
      return db.prepare(`
        SELECT *
        FROM issues
        ORDER BY created_at DESC, id DESC
        LIMIT ?
      `).all(limit);
    }

    const placeholders = severities.map(() => '?').join(', ');
    return db.prepare(`
      SELECT *
      FROM issues
      WHERE severity IN (${placeholders})
      ORDER BY created_at DESC, id DESC
      LIMIT ?
    `).all(...severities, limit);
  }

  seedDefaultAIPricing();

  return {
    db,
    saveSnapshots,
    saveIssues,
    saveAnomalies,
    saveRecentQuery,
    getRecentQueries,
    countSimilarQueries,
    hasPendingSkillProposal,
    saveSkillProposal,
    saveAIPricingRates,
    saveAIUsageEvent,
    getAIUsageSummary,
    getLatestSnapshotMap,
    getPreviousSnapshotMap,
    getRecentIssues
  };
}

function isFiniteNumber(value) {
  return typeof value === 'number' && Number.isFinite(value);
}

function toInt(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) {
    return 0;
  }
  return Math.floor(n);
}

function toNumber(value, fallback = 0) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function roundUsd(value) {
  return Number(toNumber(value, 0).toFixed(8));
}

function lookbackIso(days) {
  const safeDays = Number.isFinite(Number(days)) ? Number(days) : 0;
  return new Date(Date.now() - safeDays * 24 * 60 * 60 * 1000).toISOString();
}

module.exports = {
  initState
};
