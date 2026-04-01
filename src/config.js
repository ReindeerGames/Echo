const fs = require('fs');
const path = require('path');
const APP_ROOT = path.resolve(__dirname, '..');

function loadJsonFile(filePath, fallback = {}) {
  try {
    const content = fs.readFileSync(filePath, 'utf8');
    return JSON.parse(content);
  } catch (error) {
    console.error(`[config] Failed to load ${filePath}:`, error.message);
    return fallback;
  }
}

function loadConfig() {
  const configPath = path.join(APP_ROOT, 'config', 'echo.json');
  const defaults = {
    thresholds: {
      cpuHighPct: 85,
      memHighPct: 85,
      cpuChangePct: 25,
      memChangePct: 25,
      outlierZScore: 2,
      outlierMinCpuPct: 40,
      outlierMinMemPct: 40,
      logMaxLines: 120,
      logTail: 200,
      logLineMaxChars: 240
    },
    grouping: {
      overrides: {
        byName: {},
        byImage: {}
      }
    },
    features: {
      aiSummaries: true,
      scheduler: true,
      skillDrafting: true,
      guardedRemediation: false
    },
    scheduler: {
      cron: '*/5 * * * *'
    },
    remediation: {
      confirmationTtlSeconds: 120,
      restartTimeoutSeconds: 10
    }
  };

  const fileConfig = loadJsonFile(configPath, {});
  return {
    ...defaults,
    ...fileConfig,
    thresholds: {
      ...defaults.thresholds,
      ...(fileConfig.thresholds || {})
    },
    grouping: {
      ...defaults.grouping,
      ...(fileConfig.grouping || {}),
      overrides: {
        ...defaults.grouping.overrides,
        ...((fileConfig.grouping || {}).overrides || {})
      }
    },
    features: {
      ...defaults.features,
      ...(fileConfig.features || {})
    },
    scheduler: {
      ...defaults.scheduler,
      ...(fileConfig.scheduler || {})
    },
    remediation: {
      ...defaults.remediation,
      ...(fileConfig.remediation || {})
    }
  };
}

module.exports = {
  loadConfig,
  loadJsonFile
};
