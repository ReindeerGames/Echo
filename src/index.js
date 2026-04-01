require('dotenv').config();

const express = require('express');
const { loadConfig } = require('./config');
const { initState } = require('./state');
const {
  createDockerClient,
  listContainersWithStats,
  getContainerLogs,
  restartContainer,
  startContainer,
  findContainerByNameOrId
} = require('./docker');
const { applyGrouping, groupContainers } = require('./grouping');
const { getTopContainers, detectOutliers } = require('./metrics');
const { filterLogs } = require('./logs');
const { runDeterministicChecks, detectChanges } = require('./analysis');
const { detectIntent, shouldSkipAI, summarizeWithAI } = require('./ai');
const { normalizeQuery, maybeCreateSkillDraft } = require('./skills');
const { parseIncomingWebhook, sendWhatsAppMessage, checkWhatsAppStatus } = require('./whatsapp');
const { startScheduler } = require('./scheduler');

const PORT = Number(process.env.PORT || 3000);
const config = loadConfig();
const state = initState();
const docker = createDockerClient();
const recentInboundMessages = new Map();
const senderConversationContext = new Map();
const pendingRemediations = new Map();

const app = express();
app.use(express.json({ limit: '1mb' }));

app.get('/health', (_req, res) => {
  res.status(200).json({ status: 'ok', service: 'echo' });
});

app.post('/webhook', (req, res) => {
  const inbound = parseIncomingWebhook(req.body);

  if (!inbound.shouldProcess) {
    if (inbound.reason === 'not_whitelisted') {
      console.log(`[webhook] Ignored non-whitelisted sender: ${inbound.sender}`);
    } else if (['missing_webhook_secret', 'invalid_webhook_secret'].includes(inbound.reason)) {
      console.log(`[webhook] Ignored webhook with ${inbound.reason}`);
    }
    return res.status(200).json({ ok: true });
  }

  if (isDuplicateInbound(inbound.sender, inbound.message)) {
    console.log(`[webhook] Ignored duplicate delivery from ${inbound.sender}`);
    return res.status(200).json({ ok: true });
  }

  res.status(200).json({ ok: true });

  // Acknowledge webhook immediately to avoid upstream retries on slow investigations.
  void processInboundMessage(inbound.sender, inbound.message);
});

async function handleMessage(sender, message) {
  prunePendingRemediations();

  const detectedRoute = detectIntent(message);
  const estate = ['usage', 'identity'].includes(detectedRoute.intent) ? null : await collectEstateSnapshot();
  const route = resolveRouteWithContext(sender, message, detectedRoute, estate);

  let response;

  switch (route.intent) {
    case 'identity':
      response = buildIdentityResponse();
      break;
    case 'usage':
      response = buildUsageResponse(route.target);
      break;
    case 'priority':
      response = await buildPriorityResponse(sender, message, estate);
      break;
    case 'top':
      response = await buildTopResponse(sender, message, estate, route.target);
      break;
    case 'group_check':
      response = await buildGroupResponse(sender, message, estate, route.target);
      break;
    case 'container_check':
      response = await buildContainerResponse(sender, message, estate, route.target);
      break;
    case 'logs':
      response = await buildLogsResponse(sender, message, estate, route.target);
      break;
    case 'troubleshoot':
      response = await buildTroubleshootResponse(sender, message, estate, route.target);
      break;
    case 'remediation_request':
      response = await buildRemediationRequestResponse(sender, message, estate, route.target, route.action);
      break;
    case 'remediation_confirm':
      response = await buildRemediationConfirmResponse(sender, route.target);
      break;
    case 'remediation_cancel':
      response = buildRemediationCancelResponse(sender);
      break;
    case 'what_changed':
      response = await buildWhatChangedResponse(sender, message, estate);
      break;
    case 'full_report':
    default:
      response = await buildFullReportResponse(sender, message, estate);
      break;
  }

  response = formatForWhatsApp(route.intent, response);

  const normalizedMessage = normalizeQuery(message);
  state.saveRecentQuery({
    sender,
    intent: route.intent,
    target: route.target || null,
    message,
    normalizedMessage,
    response
  });

  const draftPath = ['usage', 'identity'].includes(route.intent)
    ? null
    : maybeCreateSkillDraft({
        state,
        message,
        intent: route.intent,
        target: route.target,
        features: config.features
      });

  if (draftPath) {
    console.log(`[skills] Draft proposal created: ${draftPath}`);
  }

  rememberConversationContext(sender, route, estate);

  return response;
}

async function processInboundMessage(sender, message) {
  try {
    const reply = await handleMessage(sender, message);
    if (reply) {
      await sendWhatsAppMessage(sender, reply);
    }
  } catch (error) {
    console.error('[webhook] Failed to process message:', error.message);
    await sendWhatsAppMessage(
      sender,
      formatForWhatsApp(
        'full_report',
        'Issue: Internal processing error occurred. Evidence: The current request failed before a full response could be produced. Recommendation: Retry in one minute, and if this repeats check service logs and API credentials.'
      )
    );
  }
}

async function collectEstateSnapshot() {
  const previous = state.getLatestSnapshotMap();

  let containers = [];
  try {
    containers = await listContainersWithStats(docker);
  } catch (error) {
    console.error('[estate] Failed to list containers:', error.message);
  }

  const groupedContainers = applyGrouping(containers, config);
  const groups = groupContainers(groupedContainers);
  const issues = runDeterministicChecks(groupedContainers, config.thresholds);
  const outliers = detectOutliers(groups, config.thresholds);
  const changes = detectChanges(groupedContainers, previous, config.thresholds);

  const ts = new Date().toISOString();
  state.saveSnapshots(groupedContainers, ts);
  state.saveIssues(issues);
  state.saveAnomalies(outliers, ts);

  return {
    ts,
    containers: groupedContainers,
    groups,
    issues,
    outliers,
    changes
  };
}

async function maybeAISummary(input) {
  const { sender, message, intent, facts, filteredLogs, estate } = input;
  const result = await summarizeWithAI({
    message,
    intent,
    facts,
    filteredLogs,
    context: buildAIContext(estate),
    allowAI: config.features.aiSummaries,
    model: process.env.OPENAI_MODEL
  });

  if (result && result.called) {
    try {
      state.saveAIUsageEvent({
        sender,
        intent,
        model: result.model || process.env.OPENAI_MODEL || 'gpt-4o-mini',
        responseId: result.responseId || null,
        requestId: result.requestId || null,
        status: result.status || (result.error ? 'error' : 'ok'),
        error: result.error || null,
        usage: result.usage || {}
      });
    } catch (error) {
      console.error('[usage] Failed to persist AI usage:', error.message);
    }
  }

  return result && result.text ? result.text : null;
}

function buildUsageResponse(target) {
  const period = String(target || 'full').toLowerCase();
  const usage = state.getAIUsageSummary();

  if (period === 'daily') {
    return [
      `Here is your daily usage snapshot for ${usage.model}.`,
      `Last 24h: ${formatUsagePeriod(usage.daily)}.`,
      `All-time: ${formatUsagePeriod(usage.all)}.`,
      `Pricing used: ${formatRateCard(usage.pricing)}.`
    ].join('\n');
  }

  if (period === 'weekly') {
    return [
      `Here is your weekly usage snapshot for ${usage.model}.`,
      `Last 7 days: ${formatUsagePeriod(usage.weekly)}.`,
      `All-time: ${formatUsagePeriod(usage.all)}.`,
      `Pricing used: ${formatRateCard(usage.pricing)}.`
    ].join('\n');
  }

  if (period === 'monthly') {
    return [
      `Here is your monthly usage snapshot for ${usage.model}.`,
      `Last 30 days: ${formatUsagePeriod(usage.monthly)}.`,
      `All-time: ${formatUsagePeriod(usage.all)}.`,
      `Pricing used: ${formatRateCard(usage.pricing)}.`
    ].join('\n');
  }

  return [
    `Here is your full usage summary for ${usage.model}.`,
    `All-time: ${formatUsagePeriod(usage.all)}.`,
    `Daily: ${formatUsagePeriod(usage.daily)}.`,
    `Weekly: ${formatUsagePeriod(usage.weekly)}.`,
    `Monthly: ${formatUsagePeriod(usage.monthly)}.`,
    `Pricing used: ${formatRateCard(usage.pricing)}.`
  ].join('\n');
}

function buildIdentityResponse() {
  const schedulerLine = config.features.scheduler
    ? `Scheduler is enabled (${config.scheduler.cron}).`
    : 'Scheduler is disabled.';
  const remediationLine = config.features.guardedRemediation
    ? `Guarded remediation is enabled (confirm window ${config.remediation.confirmationTtlSeconds}s).`
    : 'Guarded remediation is disabled.';
  const skillsLine = config.features.skillDrafting
    ? 'I can also draft repeatable investigation skills when I see recurring patterns.'
    : 'Skill drafting is disabled right now.';

  return [
    'I am Echo, your Docker SRE assistant for this host.',
    'I can monitor containers, triage priority incidents, analyze filtered logs, track changes, and troubleshoot a specific container end-to-end.',
    'I understand follow-ups like "troubleshoot it" based on recent context. Any restart/start action is guarded behind explicit confirmation.',
    `Environment: Docker socket ${process.env.DOCKER_SOCKET || '/var/run/docker.sock'}. ${schedulerLine}`,
    remediationLine,
    skillsLine,
    'Try: full report, priority, troubleshoot <container>, restart <container>, logs <container>, check <container>, group <name>, what changed, top cpu, usage.'
  ].join('\n');
}

async function buildFullReportResponse(sender, message, estate) {
  const counts = countBySeverity(estate.issues);
  const groupCount = Object.keys(estate.groups).length;
  const topCpu = getTopContainers(estate.containers, 'cpuPct', 2)
    .map((item) => `${item.name} ${formatPct(item.cpuPct)}`)
    .join(', ') || 'none';
  const topMem = getTopContainers(estate.containers, 'memPct', 2)
    .map((item) => `${item.name} ${formatPct(item.memPct)}`)
    .join(', ') || 'none';

  const facts = {
    containers: estate.containers.length,
    groups: groupCount,
    issues: counts,
    top_cpu: topCpu,
    top_mem: topMem,
    outlier_count: estate.outliers.length
  };

  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'full_report',
    facts,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  const evidence = estate.issues
    .slice(0, 3)
    .map((item) => `${item.containerName}: ${item.type}`)
    .join('; ') || 'no active high-risk findings';

  return [
    `Estate scan found ${estate.containers.length} containers across ${groupCount} groups, with ${counts.critical} critical and ${counts.high} high issues.`,
    `Evidence: ${evidence}.`,
    `Top load is CPU ${topCpu} and memory ${topMem}, with ${estate.outliers.length} outlier signals.`,
    'Recommendation: stabilize unhealthy or restarting services first, then tune sustained hot spots.'
  ].join(' ');
}

async function buildPriorityResponse(sender, message, estate) {
  const priorityIssues = estate.issues
    .filter((issue) => ['critical', 'high'].includes(issue.severity))
    .slice(0, 5);

  if (!priorityIssues.length) {
    return 'No critical or high-priority issues are active right now. Evidence: current checks show no stopped, restarting, or unhealthy containers. Recommendation: keep monitoring and run a logs check for noisy services if needed.';
  }

  const facts = priorityIssues.map((item) => ({
    container: item.containerName,
    severity: item.severity,
    type: item.type,
    evidence: item.evidence,
    recommendation: item.recommendation
  }));

  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'priority',
    facts,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  const evidence = priorityIssues.map((issue) => `${issue.containerName} (${issue.type})`).join(', ');

  return [
    `Priority queue has ${priorityIssues.length} high-severity issues requiring attention.`,
    `Evidence: ${evidence}.`,
    'Recommendation: resolve stopped or unhealthy services before performance tuning to prevent cascading failures.'
  ].join(' ');
}

async function buildTopResponse(sender, message, estate, metric) {
  const topCpu = getTopContainers(estate.containers, 'cpuPct', 5);
  const topMem = getTopContainers(estate.containers, 'memPct', 5);

  const facts = {
    metric,
    top_cpu: topCpu.map((item) => ({ name: item.name, value: item.cpuPct })),
    top_mem: topMem.map((item) => ({ name: item.name, value: item.memPct }))
  };

  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'top',
    facts,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  const cpuText = topCpu.slice(0, 3).map((item) => `${item.name} ${formatPct(item.cpuPct)}`).join(', ') || 'none';
  const memText = topMem.slice(0, 3).map((item) => `${item.name} ${formatPct(item.memPct)}`).join(', ') || 'none';

  return [
    'Top resource consumers are currently concentrated in a small set of containers.',
    `Evidence: CPU leaders are ${cpuText}, and memory leaders are ${memText}.`,
    'Recommendation: investigate sustained leaders for scaling, throttling, or application-level optimization.'
  ].join(' ');
}

async function buildGroupResponse(sender, message, estate, target) {
  if (!target) {
    return 'Group check needs a target group name. Evidence: no group identifier was found in your message. Recommendation: send a message like "group wordpress" or "check group api".';
  }

  const groupKey = resolveGroupTarget(estate.groups, target);
  if (!groupKey) {
    const available = Object.keys(estate.groups).slice(0, 8).join(', ') || 'none';
    return `I could not find group "${target}". Evidence: available groups are ${available}. Recommendation: retry with one of those group names or adjust grouping overrides in config/echo.json.`;
  }

  const containers = estate.groups[groupKey] || [];
  const running = containers.filter((item) => item.state === 'running').length;
  const unhealthy = containers.filter((item) => item.health === 'unhealthy').length;
  const restarting = containers.filter((item) => item.restarting || item.state === 'restarting').length;

  const topCpu = getTopContainers(containers, 'cpuPct', 2)
    .map((item) => `${item.name} ${formatPct(item.cpuPct)}`)
    .join(', ') || 'none';

  const groupIssues = estate.issues.filter((issue) => String(issue.group || '') === String(groupKey));

  const facts = {
    group: groupKey,
    container_count: containers.length,
    running,
    unhealthy,
    restarting,
    issue_count: groupIssues.length,
    top_cpu: topCpu
  };

  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'group_check',
    facts,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  return [
    `Group ${groupKey} has ${containers.length} containers with ${running} running, ${unhealthy} unhealthy, and ${restarting} restarting.`,
    `Evidence: top CPU in the group is ${topCpu}, and ${groupIssues.length} issues are currently associated with this group.`,
    'Recommendation: stabilize unhealthy or restarting members, then investigate high-load nodes in this group.'
  ].join(' ');
}

async function buildContainerResponse(sender, message, estate, target) {
  if (!target) {
    return 'Container check needs a container name or ID. Evidence: no target was detected in your message. Recommendation: send "check <container>" or "status <container>".';
  }

  const container = findContainerByNameOrId(estate.containers, target);
  if (!container) {
    return `I could not find container "${target}". Evidence: no running or known container matched that identifier. Recommendation: use an exact name, short ID, or run a full report.`;
  }

  if (shouldSkipAI(container)) {
    return [
      `${container.name} is in a degraded state (${container.state}, health ${container.health}).`,
      `Evidence: restart flag is ${container.restarting ? 'on' : 'off'}, CPU is ${formatPct(container.cpuPct)}, and memory is ${formatPct(container.memPct)}.`,
      `Recommendation: inspect ${container.name} startup and healthcheck dependencies immediately before any broader tuning.`
    ].join(' ');
  }

  const facts = {
    name: container.name,
    state: container.state,
    health: container.health,
    cpu_pct: container.cpuPct,
    mem_pct: container.memPct,
    group: container.group
  };

  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'container_check',
    facts,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  return [
    `${container.name} is currently ${container.state} with health ${container.health}.`,
    `Evidence: CPU is ${formatPct(container.cpuPct)} and memory is ${formatPct(container.memPct)} in group ${container.group}.`,
    `Recommendation: continue monitoring if stable, and run logs ${container.name} if latency or errors are observed.`
  ].join(' ');
}

async function buildLogsResponse(sender, message, estate, target) {
  if (!target) {
    return 'Logs check needs a container target. Evidence: no container was included in your request. Recommendation: send "logs <container>" to continue.';
  }

  const container = findContainerByNameOrId(estate.containers, target);
  if (!container) {
    return `I could not find container "${target}" for logs. Evidence: no matching container is visible right now. Recommendation: verify the name or run a full report first.`;
  }

  let rawLines = [];
  try {
    rawLines = await getContainerLogs(docker, container.id, {
      tail: config.thresholds.logTail,
      since: 0
    });
  } catch (error) {
    console.error(`[logs] Failed to fetch logs for ${container.name}:`, error.message);
    return `I could not fetch logs for ${container.name}. Evidence: Docker log retrieval failed on this host. Recommendation: verify Docker socket permissions and retry.`;
  }

  const filtered = filterLogs(rawLines, {
    maxLines: config.thresholds.logMaxLines,
    maxChars: config.thresholds.logLineMaxChars
  });

  const signals = summarizeLogSignals(filtered.lines);
  const facts = {
    container: container.name,
    state: container.state,
    health: container.health,
    filtered_stats: filtered.stats,
    signals
  };

  if (!shouldSkipAI(container)) {
    const aiSummary = await maybeAISummary({
      sender,
      message,
      intent: 'logs',
      facts,
      filteredLogs: signals,
      estate
    });

    if (aiSummary) {
      return aiSummary;
    }
  }

  return [
    `Log triage for ${container.name} shows ${signals.errors} error signatures and ${signals.warnings} warning signatures across ${filtered.stats.selectedLines} filtered lines.`,
    `Evidence: timeout mentions ${signals.timeouts}, connection-failure mentions ${signals.refused}, and OOM signals ${signals.oom}.`,
    `Container state is ${container.state} with health ${container.health}.`,
    `Recommendation: address the dominant error signatures first, then re-check logs after remediation.`
  ].join(' ');
}

async function buildTroubleshootResponse(sender, message, estate, target) {
  if (!target) {
    return 'I can troubleshoot this now, but I need a container target. Send "troubleshoot <container>" or ask "priority" first and then say "troubleshoot it".';
  }

  const container = findContainerByNameOrId(estate.containers, target);
  if (!container) {
    return `I could not find container "${target}" to troubleshoot. Try the exact name/ID, or run "full report" to see active containers first.`;
  }

  let rawLines = [];
  try {
    rawLines = await getContainerLogs(docker, container.id, {
      tail: config.thresholds.logTail,
      since: 0
    });
  } catch (error) {
    console.error(`[troubleshoot] Failed to fetch logs for ${container.name}:`, error.message);
  }

  const filtered = filterLogs(rawLines, {
    maxLines: config.thresholds.logMaxLines,
    maxChars: config.thresholds.logLineMaxChars
  });
  const signals = summarizeLogSignals(filtered.lines);
  const relatedIssues = estate.issues
    .filter((issue) => issue.containerId === container.id || issue.containerName === container.name)
    .slice(0, 6);
  const relatedChanges = estate.changes
    .filter((item) => String(item.containerName || '') === String(container.name))
    .slice(0, 5);

  const facts = {
    container: container.name,
    state: container.state,
    health: container.health,
    restarting: container.restarting,
    restart_count: container.restartCount,
    group: container.group,
    cpu_pct: container.cpuPct,
    mem_pct: container.memPct,
    related_issues: relatedIssues.map((item) => ({
      severity: item.severity,
      type: item.type,
      evidence: item.evidence
    })),
    recent_changes: relatedChanges,
    filtered_log_stats: filtered.stats,
    log_signals: signals
  };

  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'troubleshoot',
    facts,
    filteredLogs: signals,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  const likelyCause = inferLikelyCause(container, signals, relatedIssues);
  const issueSummary = relatedIssues.length
    ? relatedIssues.map((item) => `${item.severity}/${item.type}`).join(', ')
    : 'none detected from deterministic checks';
  const changeSummary = relatedChanges.length
    ? relatedChanges.map((item) => describeChange(item)).join('; ')
    : 'no recent high-signal changes';

  return [
    `Troubleshooting outcome for ${container.name}: likely cause is ${likelyCause}.`,
    `Current state: ${container.state}, health ${container.health}, restart count ${container.restartCount}, CPU ${formatPct(container.cpuPct)}, memory ${formatPct(container.memPct)}.`,
    `Checks: issues=${issueSummary}; logs show errors=${signals.errors}, warnings=${signals.warnings}, timeouts=${signals.timeouts}, refused=${signals.refused}, oom=${signals.oom}; recent changes=${changeSummary}.`,
    `Next actions: 1) verify dependency readiness and startup config for ${container.name}; 2) re-run logs and status after remediation to confirm recovery.`
  ].join('\n');
}

async function buildRemediationRequestResponse(sender, message, estate, target, requestedAction) {
  if (!config.features.guardedRemediation) {
    return 'Guarded remediation is currently disabled. I can still troubleshoot and suggest exact next actions.';
  }

  const pending = getPendingRemediation(sender);
  if (pending) {
    return [
      `You already have a pending action for ${pending.containerName}: ${pending.action}.`,
      `Reply "confirm ${pending.code}" to execute, or "cancel" to discard it.`
    ].join('\n');
  }

  if (!target) {
    return 'Remediation needs a container target. Send "restart <container>" or "start <container>", then I will ask for explicit confirmation.';
  }

  const container = findContainerByNameOrId(estate.containers, target);
  if (!container) {
    return `I could not find container "${target}" for remediation. Run "full report" first to confirm the container name.`;
  }

  const plan = chooseSafeRemediationAction(container, requestedAction);
  if (!plan.action) {
    return [
      `No action was queued for ${container.name}.`,
      plan.reason || 'Container is already in the requested state.'
    ].join('\n');
  }

  const ttlSeconds = Math.max(30, Number(config.remediation.confirmationTtlSeconds || 120));
  const code = generateConfirmationCode();
  const expiresAt = Date.now() + ttlSeconds * 1000;

  pendingRemediations.set(sender, {
    sender,
    code,
    action: plan.action,
    requestedAction: requestedAction || plan.action,
    rationale: plan.reason,
    containerId: container.id,
    containerName: container.name,
    createdAt: Date.now(),
    expiresAt
  });

  return [
    `Prepared guarded action: ${plan.action} ${container.name}.`,
    `Current state: ${container.state}, health ${container.health}, restart count ${container.restartCount}.`,
    `Reason: ${plan.reason}.`,
    `No change made yet. Reply "confirm ${code}" within ${ttlSeconds}s to execute, or "cancel" to stop.`
  ].join('\n');
}

async function buildRemediationConfirmResponse(sender, confirmationCode) {
  if (!config.features.guardedRemediation) {
    return 'Guarded remediation is disabled.';
  }

  const pending = getPendingRemediation(sender);
  if (!pending) {
    return 'No pending guarded action was found. Send "restart <container>" or "start <container>" first.';
  }

  if (!confirmationCode) {
    return `Confirmation code required. Reply "confirm ${pending.code}" to execute, or "cancel" to discard.`;
  }

  if (String(confirmationCode).toUpperCase() !== pending.code) {
    return `Confirmation code mismatch. Reply "confirm ${pending.code}" to execute, or "cancel" to discard.`;
  }

  if (Date.now() > pending.expiresAt) {
    pendingRemediations.delete(sender);
    return 'The confirmation window expired. Send the remediation request again if you still want me to proceed.';
  }

  pendingRemediations.delete(sender);

  try {
    if (pending.action === 'restart') {
      await restartContainer(docker, pending.containerId, {
        timeoutSec: config.remediation.restartTimeoutSeconds
      });
    } else if (pending.action === 'start') {
      await startContainer(docker, pending.containerId);
    } else {
      return `Unsupported remediation action "${pending.action}".`;
    }
  } catch (error) {
    return [
      `I attempted ${pending.action} on ${pending.containerName}, but Docker returned an error.`,
      `Error: ${error.message || 'unknown error'}.`,
      'No further action was taken.'
    ].join('\n');
  }

  const container = await fetchContainerStateById(pending.containerId);
  if (!container) {
    return [
      `Executed ${pending.action} on ${pending.containerName}.`,
      'Post-action state could not be refreshed yet. Run "check <container>" in a few seconds for verification.'
    ].join('\n');
  }

  return [
    `Executed ${pending.action} on ${container.name}.`,
    `Current state: ${container.state}, health ${container.health}, restart count ${container.restartCount}, CPU ${formatPct(container.cpuPct)}, memory ${formatPct(container.memPct)}.`,
    'Verification complete. If you want, I can run a focused log triage next.'
  ].join('\n');
}

function buildRemediationCancelResponse(sender) {
  const pending = getPendingRemediation(sender);
  if (!pending) {
    return 'There is no pending guarded action to cancel.';
  }

  pendingRemediations.delete(sender);
  return `Cancelled pending action: ${pending.action} ${pending.containerName}.`;
}

async function buildWhatChangedResponse(sender, message, estate) {
  if (!estate.changes.length) {
    return 'No material state or resource changes were detected since the previous snapshot. Evidence: container states and key CPU or memory deltas stayed within configured thresholds. Recommendation: continue periodic monitoring and request top consumers if performance concerns persist.';
  }

  const sorted = estate.changes
    .slice()
    .sort((a, b) => severityScore(b.severity) - severityScore(a.severity));

  const facts = sorted.slice(0, 8);
  const aiSummary = await maybeAISummary({
    sender,
    message,
    intent: 'what_changed',
    facts,
    estate
  });

  if (aiSummary) {
    return aiSummary;
  }

  const evidence = sorted
    .slice(0, 4)
    .map((change) => describeChange(change))
    .join('; ');

  return [
    `Detected ${estate.changes.length} notable changes since the last snapshot.`,
    `Evidence: ${evidence}.`,
    'Recommendation: prioritize high-severity state and health transitions before tuning metric-only changes.'
  ].join(' ');
}

function summarizeLogSignals(lines) {
  const joined = lines.join('\n').toLowerCase();

  return {
    errors: countMatches(joined, /error|exception|failed|failure/g),
    warnings: countMatches(joined, /warn|warning|retry|degraded/g),
    timeouts: countMatches(joined, /timeout|timed out/g),
    refused: countMatches(joined, /connection refused|econnrefused|refused/g),
    oom: countMatches(joined, /out of memory|oom|killed process/g)
  };
}

function inferLikelyCause(container, signals, relatedIssues) {
  if (signals.oom > 0 || relatedIssues.some((item) => item.type === 'memory_high')) {
    return 'memory pressure or OOM termination';
  }

  const state = String(container.state || '').toLowerCase();
  const health = String(container.health || '').toLowerCase();

  if (container.restarting || state === 'restarting') {
    return 'a crash loop from recurring startup failure';
  }

  if (['exited', 'dead'].includes(state)) {
    if (signals.refused > 0 || signals.timeouts > 0) {
      return 'startup dependency failure (connection refused/timeout)';
    }
    if (signals.errors > 0) {
      return 'application or configuration startup failure';
    }
    return 'container exit without sustained runtime';
  }

  if (health === 'unhealthy') {
    if (signals.timeouts > 0 || signals.refused > 0) {
      return 'healthcheck dependency timeout/refusal';
    }
    return 'healthcheck failure';
  }

  if (relatedIssues.some((item) => item.type === 'cpu_high')) {
    return 'sustained CPU saturation';
  }

  return 'no single dominant failure signature yet';
}

function resolveRouteWithContext(sender, message, route, estate) {
  if (!route || route.target || !estate) {
    return route;
  }

  if (!['container_check', 'logs', 'troubleshoot', 'remediation_request'].includes(route.intent)) {
    return route;
  }

  if (route.intent === 'remediation_request' && !referencesPreviousTarget(message)) {
    return route;
  }

  if (!referencesPreviousTarget(message) && route.intent !== 'troubleshoot') {
    return route;
  }

  const fromContext = getRecentContainerContext(sender);
  if (fromContext) {
    return {
      ...route,
      target: fromContext
    };
  }

  if (route.intent !== 'remediation_request') {
    const fromIssues = pickPrimaryIssueContainer(estate);
    if (fromIssues) {
      return {
        ...route,
        target: fromIssues
      };
    }
  }

  return route;
}

function getPendingRemediation(sender) {
  if (!sender) {
    return null;
  }

  const pending = pendingRemediations.get(sender);
  if (!pending) {
    return null;
  }

  if (Date.now() > Number(pending.expiresAt || 0)) {
    pendingRemediations.delete(sender);
    return null;
  }

  return pending;
}

function prunePendingRemediations() {
  const now = Date.now();
  for (const [sender, pending] of pendingRemediations.entries()) {
    if (!pending || now > Number(pending.expiresAt || 0)) {
      pendingRemediations.delete(sender);
    }
  }
}

function chooseSafeRemediationAction(container, requestedAction) {
  const state = String(container.state || '').toLowerCase();
  const desired = String(requestedAction || 'restart').toLowerCase();

  if (desired === 'start') {
    if (state === 'running') {
      return {
        action: null,
        reason: `${container.name} is already running`
      };
    }
    return {
      action: 'start',
      reason: `${container.name} is not running, so start is the direct recovery action`
    };
  }

  if (['created', 'exited', 'dead', 'paused'].includes(state)) {
    return {
      action: 'start',
      reason: `${container.name} is ${state}; start is safer than restart from this state`
    };
  }

  return {
    action: 'restart',
    reason: `${container.name} is active; controlled restart can recover transient runtime faults`
  };
}

function generateConfirmationCode() {
  return Math.random().toString(36).slice(2, 8).toUpperCase();
}

async function fetchContainerStateById(containerId) {
  if (!containerId) {
    return null;
  }

  try {
    const containers = await listContainersWithStats(docker);
    const grouped = applyGrouping(containers, config);
    return grouped.find((item) => item.id === containerId) || null;
  } catch (error) {
    console.error('[remediation] Failed to refresh container state:', error.message);
    return null;
  }
}

function rememberConversationContext(sender, route, estate) {
  if (!sender || !route) {
    return;
  }

  const existing = senderConversationContext.get(sender) || {};
  const intentsWithContainerTarget = new Set([
    'container_check',
    'logs',
    'troubleshoot',
    'remediation_request'
  ]);
  const resolvedTarget = estate
    && intentsWithContainerTarget.has(route.intent)
    ? resolveCanonicalContainerName(estate.containers, route.target)
    : null;
  const issueTarget = estate ? pickPrimaryIssueContainer(estate) : null;

  const next = {
    ...existing,
    updatedAt: Date.now(),
    lastIntent: route.intent
  };

  if (resolvedTarget) {
    next.lastContainerName = resolvedTarget;
  } else if (['priority', 'full_report', 'troubleshoot'].includes(route.intent) && issueTarget) {
    next.lastContainerName = issueTarget;
  }

  senderConversationContext.set(sender, next);
  pruneSenderContextMap();
}

function referencesPreviousTarget(message) {
  const text = String(message || '').toLowerCase();
  return /\b(it|this|that|that one|this one|the issue|the container|that container|same one)\b/.test(text);
}

function getRecentContainerContext(sender, maxAgeMs = 6 * 60 * 60 * 1000) {
  const context = senderConversationContext.get(sender);
  if (!context || !context.lastContainerName) {
    return null;
  }

  const age = Date.now() - Number(context.updatedAt || 0);
  if (!Number.isFinite(age) || age > maxAgeMs) {
    return null;
  }

  return context.lastContainerName;
}

function pruneSenderContextMap(maxEntries = 200, maxAgeMs = 24 * 60 * 60 * 1000) {
  const now = Date.now();

  for (const [sender, context] of senderConversationContext.entries()) {
    const age = now - Number(context && context.updatedAt ? context.updatedAt : 0);
    if (!Number.isFinite(age) || age > maxAgeMs) {
      senderConversationContext.delete(sender);
    }
  }

  if (senderConversationContext.size <= maxEntries) {
    return;
  }

  const rows = Array.from(senderConversationContext.entries())
    .sort((a, b) => Number(a[1].updatedAt || 0) - Number(b[1].updatedAt || 0));
  const overflow = senderConversationContext.size - maxEntries;
  for (let i = 0; i < overflow; i += 1) {
    senderConversationContext.delete(rows[i][0]);
  }
}

function pickPrimaryIssueContainer(estate) {
  if (!estate || !Array.isArray(estate.issues)) {
    return null;
  }

  const issue = estate.issues
    .slice()
    .sort((a, b) => severityScore(b.severity) - severityScore(a.severity))
    .find((item) => item.containerName);

  if (issue && issue.containerName) {
    return issue.containerName;
  }

  if (estate.containers.length === 1) {
    return estate.containers[0].name;
  }

  return null;
}

function resolveCanonicalContainerName(containers, target) {
  const needle = String(target || '').trim();
  if (!needle) {
    return null;
  }

  const match = findContainerByNameOrId(containers || [], needle);
  return match ? match.name : needle;
}

function buildAIContext(estate) {
  return {
    now_utc_iso: new Date().toISOString(),
    capabilities: [
      'Container, group, priority, and change diagnostics',
      'Filtered log triage and signature counting',
      'Troubleshooting summaries from deterministic facts',
      'Optional guarded start/restart actions with explicit confirmation'
    ],
    constraints: [
      'Read-only diagnostics mode by default',
      'No raw log dump; filtered signals only',
      'Do not invent data when telemetry is missing'
    ],
    environment: {
      docker_socket: process.env.DOCKER_SOCKET || '/var/run/docker.sock',
      scheduler_enabled: Boolean(config.features.scheduler),
      scheduler_cron: config.features.scheduler ? config.scheduler.cron : null,
      ai_summaries_enabled: Boolean(config.features.aiSummaries),
      skill_drafting_enabled: Boolean(config.features.skillDrafting),
      guarded_remediation_enabled: Boolean(config.features.guardedRemediation),
      remediation_confirmation_ttl_seconds: Number(config.remediation.confirmationTtlSeconds || 0),
      thresholds: config.thresholds
    },
    estate_scope: estate
      ? {
          container_count: estate.containers.length,
          group_count: Object.keys(estate.groups || {}).length,
          issue_count: estate.issues.length,
          outlier_count: estate.outliers.length,
          groups_sample: Object.keys(estate.groups || {}).slice(0, 8)
        }
      : null
  };
}

function formatForWhatsApp(intent, rawText) {
  const title = getIntentTitle(intent);
  const normalized = normalizeMessageText(rawText);
  const body = normalized;
  return [`*Echo | ${title}*`, '', body].join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

function getIntentTitle(intent) {
  const map = {
    identity: 'Identity',
    usage: 'AI Usage',
    full_report: 'Full Report',
    priority: 'Priority',
    top: 'Top Resources',
    group_check: 'Group Check',
    container_check: 'Container Check',
    troubleshoot: 'Troubleshoot',
    remediation_request: 'Guarded Action',
    remediation_confirm: 'Guarded Action',
    remediation_cancel: 'Guarded Action',
    logs: 'Log Triage',
    what_changed: 'What Changed'
  };

  return map[intent] || 'Status';
}

function normalizeMessageText(text) {
  const cleaned = cleanMessageText(text)
    .replace(/\*\*/g, '')
    .replace(/\bIssue:\s*/gi, '')
    .replace(/\bEvidence:\s*/gi, '')
    .replace(/\s*;\s*/g, '; ')
    .trim();

  if (!cleaned) {
    return 'No update available yet.';
  }

  return cleaned;
}

function formatNarrativeWithHeadings(text) {
  const cleaned = stripConversationalTail(normalizeMessageText(text));
  const sentences = splitSentences(cleaned);

  if (!sentences.length) {
    return 'No update available yet.';
  }

  const nextStepIndex = findNextStepSentenceIndex(sentences);
  const summary = sentences[0];
  const details = sentences
    .filter((_, index) => index !== 0 && index !== nextStepIndex)
    .slice(0, 3);
  const nextStep = nextStepIndex >= 0
    ? stripNextStepPrefix(sentences[nextStepIndex])
    : defaultNextStep(sentences);

  const lines = [
    '*Quick update*',
    summary
  ];

  if (details.length) {
    lines.push('');
    lines.push('*What I’m seeing*');
    for (const item of details) {
      lines.push(`- ${item}`);
    }
  }

  if (nextStep) {
    lines.push('');
    lines.push('*Next step*');
    lines.push(nextStep);
  }

  return lines.join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

function splitSentences(text) {
  const lines = String(text || '').split(/\n+/).map((line) => line.trim()).filter(Boolean);
  const out = [];

  for (const line of lines) {
    let current = '';
    for (let i = 0; i < line.length; i += 1) {
      const ch = line[i];
      const prev = i > 0 ? line[i - 1] : '';
      const next = i + 1 < line.length ? line[i + 1] : '';
      current += ch;

      const isSentencePunct = ch === '.' || ch === '!' || ch === '?';
      const isDecimalPoint = ch === '.' && /\d/.test(prev) && /\d/.test(next);

      if (isSentencePunct && !isDecimalPoint) {
        const candidate = cleanMessageText(current).replace(/^[-*]\s*/, '');
        if (candidate) {
          out.push(candidate);
        }
        current = '';
      }
    }

    const tail = cleanMessageText(current).replace(/^[-*]\s*/, '');
    if (tail) {
      out.push(tail);
    }
  }

  return out;
}

function findNextStepSentenceIndex(sentences) {
  const actionPattern = /\b(next step|focus|check|investigate|review|verify|monitor|restart|scale|tune|triage)\b/i;
  for (let i = sentences.length - 1; i >= 0; i -= 1) {
    if (actionPattern.test(sentences[i])) {
      return i;
    }
  }
  return -1;
}

function stripNextStepPrefix(text) {
  const cleaned = cleanMessageText(String(text || ''))
    .replace(/^next step:\s*/i, '')
    .replace(/^recommendation:\s*/i, '');
  if (!cleaned) {
    return '';
  }
  return cleaned.charAt(0).toUpperCase() + cleaned.slice(1);
}

function defaultNextStep(sentences) {
  const fallback = sentences[sentences.length - 1] || '';
  const cleaned = stripNextStepPrefix(fallback);
  if (cleaned && cleaned.length > 12) {
    return cleaned;
  }
  return 'If you want, ask for logs on a specific container and I will drill in.';
}

function stripConversationalTail(text) {
  return String(text || '')
    .replace(/\blet me know if you need specifics\.?$/i, '')
    .replace(/\blet me know if you want details\.?$/i, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function cleanMessageText(value) {
  return String(value || '')
    .replace(/\r/g, '')
    .replace(/[ \t]+/g, ' ')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

function formatUsagePeriod(item) {
  return `${formatNumber(item.calls)} calls, ${formatNumber(item.inputTokens)} input tokens, ${formatNumber(item.outputTokens)} output tokens, ${formatUsd(item.totalCostUsd)}`;
}

function formatRateCard(pricing) {
  if (!pricing) {
    return 'pricing not configured';
  }

  return `input ${formatUsd(pricing.inputPerMillion)}/1M, cached input ${formatUsd(pricing.cachedInputPerMillion)}/1M, output ${formatUsd(pricing.outputPerMillion)}/1M`;
}

function resolveGroupTarget(groups, target) {
  const keys = Object.keys(groups);
  const needle = String(target || '').toLowerCase();

  const exact = keys.find((key) => key.toLowerCase() === needle);
  if (exact) {
    return exact;
  }

  return keys.find((key) => key.toLowerCase().includes(needle));
}

function countBySeverity(issues) {
  const counts = {
    critical: 0,
    high: 0,
    medium: 0,
    low: 0
  };

  for (const issue of issues) {
    if (counts[issue.severity] !== undefined) {
      counts[issue.severity] += 1;
    }
  }

  return counts;
}

function describeChange(change) {
  if (change.type === 'state_change') {
    return `${change.containerName} state ${change.from} -> ${change.to}`;
  }

  if (change.type === 'health_change') {
    return `${change.containerName} health ${change.from} -> ${change.to}`;
  }

  if (change.type === 'cpu_change') {
    return `${change.containerName} CPU ${formatPct(change.from)} -> ${formatPct(change.to)}`;
  }

  if (change.type === 'memory_change') {
    return `${change.containerName} memory ${formatPct(change.from)} -> ${formatPct(change.to)}`;
  }

  return `${change.containerName} changed`;
}

function formatPct(value) {
  if (!isFiniteNumber(value)) {
    return 'n/a';
  }
  return `${Number(value).toFixed(1)}%`;
}

function formatNumber(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) {
    return '0';
  }
  return Math.floor(n).toLocaleString('en-US');
}

function formatUsd(value) {
  const n = Number(value);
  const safe = Number.isFinite(n) ? n : 0;
  if (safe >= 1) {
    return `$${safe.toFixed(2)}`;
  }
  if (safe >= 0.01) {
    return `$${safe.toFixed(4)}`;
  }
  return `$${safe.toFixed(6)}`;
}

function countMatches(text, regex) {
  const matches = String(text || '').match(regex);
  return matches ? matches.length : 0;
}

function severityScore(severity) {
  if (severity === 'critical') {
    return 4;
  }
  if (severity === 'high') {
    return 3;
  }
  if (severity === 'medium') {
    return 2;
  }
  return 1;
}

function isFiniteNumber(value) {
  return typeof value === 'number' && Number.isFinite(value);
}

function isDuplicateInbound(sender, message, windowMs = 20000) {
  const now = Date.now();
  const key = `${sender}:${normalizeQuery(message)}`;
  const previousTs = recentInboundMessages.get(key) || 0;

  // Lightweight cleanup to prevent growth from old keys.
  for (const [storedKey, ts] of recentInboundMessages.entries()) {
    if (now - ts > windowMs * 5) {
      recentInboundMessages.delete(storedKey);
    }
  }

  if (now - previousTs <= windowMs) {
    return true;
  }

  recentInboundMessages.set(key, now);
  return false;
}

async function bootstrap() {
  const waStatus = await checkWhatsAppStatus();
  if (waStatus.ok) {
    const attemptsInfo = waStatus.attempts && waStatus.attempts > 1
      ? ` (after ${waStatus.attempts} attempts)`
      : '';
    console.log(`[startup] WhatsApp status: ${waStatus.status}/${waStatus.substatus}${attemptsInfo}`);
  } else {
    console.log(`[startup] WhatsApp status unavailable: ${waStatus.reason}`);
  }

  await collectEstateSnapshot();

  if (config.features.scheduler) {
    startScheduler({
      cronExpr: config.scheduler.cron,
      onTick: collectEstateSnapshot
    });
  }

  app.listen(PORT, () => {
    console.log(`[startup] Echo listening on port ${PORT}`);
  });
}

process.on('uncaughtException', (error) => {
  console.error('[fatal] uncaughtException:', error);
});

process.on('unhandledRejection', (error) => {
  console.error('[fatal] unhandledRejection:', error);
});

bootstrap().catch((error) => {
  console.error('[startup] Failed to start Echo:', error.message);
});
