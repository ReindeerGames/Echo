function runDeterministicChecks(containers, thresholds) {
  const issues = [];
  const cpuHigh = numberOr(thresholds.cpuHighPct, 85);
  const memHigh = numberOr(thresholds.memHighPct, 85);

  for (const container of containers) {
    const state = String(container.state || '').toLowerCase();
    const health = String(container.health || 'none').toLowerCase();

    if (isStoppedState(state, container.statusText)) {
      issues.push({
        issueKey: `stopped:${container.id}`,
        type: 'container_stopped',
        severity: 'critical',
        containerId: container.id,
        containerName: container.name,
        group: container.group,
        evidence: `${container.name} is in ${state || 'stopped'} state`,
        recommendation: `Start ${container.name} and inspect startup logs for root cause.`
      });
    }

    if (container.restarting || state === 'restarting') {
      issues.push({
        issueKey: `restarting:${container.id}`,
        type: 'container_restarting',
        severity: 'high',
        containerId: container.id,
        containerName: container.name,
        group: container.group,
        evidence: `${container.name} is restarting repeatedly`,
        recommendation: `Check crash loop reason and apply config or dependency fix before next restart.`
      });
    }

    if (health === 'unhealthy') {
      issues.push({
        issueKey: `unhealthy:${container.id}`,
        type: 'container_unhealthy',
        severity: 'high',
        containerId: container.id,
        containerName: container.name,
        group: container.group,
        evidence: `${container.name} healthcheck status is unhealthy`,
        recommendation: `Inspect health endpoint dependencies and readiness probe failures for ${container.name}.`
      });
    }

    if (isFiniteNumber(container.cpuPct) && container.cpuPct >= cpuHigh) {
      issues.push({
        issueKey: `cpu_high:${container.id}`,
        type: 'cpu_high',
        severity: container.cpuPct >= 95 ? 'high' : 'medium',
        containerId: container.id,
        containerName: container.name,
        group: container.group,
        evidence: `${container.name} CPU at ${round(container.cpuPct)}% exceeds ${cpuHigh}%`,
        recommendation: `Profile ${container.name} workload, then scale or tune limits if sustained.`
      });
    }

    if (isFiniteNumber(container.memPct) && container.memPct >= memHigh) {
      issues.push({
        issueKey: `mem_high:${container.id}`,
        type: 'memory_high',
        severity: container.memPct >= 95 ? 'high' : 'medium',
        containerId: container.id,
        containerName: container.name,
        group: container.group,
        evidence: `${container.name} memory at ${round(container.memPct)}% exceeds ${memHigh}%`,
        recommendation: `Check memory leaks or cache growth, then adjust memory limit if justified.`
      });
    }
  }

  return dedupeIssues(issues);
}

function detectChanges(containers, previousSnapshotMap, thresholds) {
  const changes = [];
  const cpuChange = numberOr(thresholds.cpuChangePct, 25);
  const memChange = numberOr(thresholds.memChangePct, 25);

  for (const container of containers) {
    const prev =
      previousSnapshotMap.get(container.id) ||
      previousSnapshotMap.get(String(container.name || '').toLowerCase());

    if (!prev) {
      continue;
    }

    if (String(prev.state || '') !== String(container.state || '')) {
      changes.push({
        type: 'state_change',
        containerName: container.name,
        from: prev.state,
        to: container.state,
        severity: 'high'
      });
    }

    if (
      String(prev.health || 'none') !== String(container.health || 'none') &&
      String(container.health || 'none') !== 'none'
    ) {
      changes.push({
        type: 'health_change',
        containerName: container.name,
        from: prev.health || 'none',
        to: container.health || 'none',
        severity: String(container.health || '').toLowerCase() === 'unhealthy' ? 'high' : 'medium'
      });
    }

    if (isFiniteNumber(container.cpuPct) && isFiniteNumber(prev.cpu_pct)) {
      const delta = round(container.cpuPct - prev.cpu_pct);
      if (Math.abs(delta) >= cpuChange) {
        changes.push({
          type: 'cpu_change',
          containerName: container.name,
          delta,
          from: round(prev.cpu_pct),
          to: round(container.cpuPct),
          severity: Math.abs(delta) >= 40 ? 'high' : 'medium'
        });
      }
    }

    if (isFiniteNumber(container.memPct) && isFiniteNumber(prev.mem_pct)) {
      const delta = round(container.memPct - prev.mem_pct);
      if (Math.abs(delta) >= memChange) {
        changes.push({
          type: 'memory_change',
          containerName: container.name,
          delta,
          from: round(prev.mem_pct),
          to: round(container.memPct),
          severity: Math.abs(delta) >= 40 ? 'high' : 'medium'
        });
      }
    }
  }

  return changes;
}

function isStoppedState(state, statusText) {
  if (['exited', 'dead', 'paused'].includes(state)) {
    return true;
  }

  const text = String(statusText || '').toLowerCase();
  return text.includes('exited') || text.includes('dead');
}

function dedupeIssues(issues) {
  const map = new Map();
  for (const issue of issues) {
    map.set(issue.issueKey, issue);
  }
  return Array.from(map.values());
}

function isFiniteNumber(value) {
  return typeof value === 'number' && Number.isFinite(value);
}

function numberOr(value, fallback) {
  return isFiniteNumber(value) ? value : fallback;
}

function round(value) {
  return Number(Number(value).toFixed(2));
}

module.exports = {
  runDeterministicChecks,
  detectChanges
};
