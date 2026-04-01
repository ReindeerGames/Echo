function getTopContainers(containers, metric, count = 5) {
  return containers
    .filter((container) => isFiniteNumber(container[metric]))
    .slice()
    .sort((a, b) => b[metric] - a[metric])
    .slice(0, count);
}

function detectOutliers(groups, thresholds) {
  const outliers = [];
  const zThreshold = numberOr(thresholds.outlierZScore, 2);
  const minCpu = numberOr(thresholds.outlierMinCpuPct, 40);
  const minMem = numberOr(thresholds.outlierMinMemPct, 40);

  for (const [group, containers] of Object.entries(groups)) {
    if (!containers.length) {
      continue;
    }

    const cpuStats = summarize(containers.map((container) => container.cpuPct));
    const memStats = summarize(containers.map((container) => container.memPct));

    for (const container of containers) {
      if (cpuStats.std > 0 && isFiniteNumber(container.cpuPct)) {
        const z = (container.cpuPct - cpuStats.mean) / cpuStats.std;
        if (z >= zThreshold && container.cpuPct >= minCpu) {
          outliers.push({
            containerId: container.id,
            containerName: container.name,
            group,
            metric: 'cpu_pct',
            value: round(container.cpuPct),
            baseline: round(cpuStats.mean),
            zScore: round(z),
            note: `CPU usage above group baseline in ${group}`
          });
        }
      }

      if (memStats.std > 0 && isFiniteNumber(container.memPct)) {
        const z = (container.memPct - memStats.mean) / memStats.std;
        if (z >= zThreshold && container.memPct >= minMem) {
          outliers.push({
            containerId: container.id,
            containerName: container.name,
            group,
            metric: 'mem_pct',
            value: round(container.memPct),
            baseline: round(memStats.mean),
            zScore: round(z),
            note: `Memory usage above group baseline in ${group}`
          });
        }
      }
    }
  }

  return outliers;
}

function summarize(values) {
  const clean = values.filter(isFiniteNumber);
  if (!clean.length) {
    return { mean: 0, std: 0 };
  }

  const mean = clean.reduce((acc, value) => acc + value, 0) / clean.length;
  const variance =
    clean.reduce((acc, value) => acc + Math.pow(value - mean, 2), 0) / clean.length;

  return {
    mean,
    std: Math.sqrt(variance)
  };
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
  getTopContainers,
  detectOutliers
};
