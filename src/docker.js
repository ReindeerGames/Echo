const Docker = require('dockerode');

function createDockerClient() {
  const socketPath = process.env.DOCKER_SOCKET || '/var/run/docker.sock';
  return new Docker({ socketPath });
}

async function listContainersWithStats(docker) {
  const summaries = await docker.listContainers({ all: true });
  const containers = await Promise.all(
    summaries.map(async (summary) => enrichContainer(docker, summary))
  );
  return containers;
}

async function enrichContainer(docker, summary) {
  const container = docker.getContainer(summary.Id);

  let inspect = null;
  try {
    inspect = await withTimeout(container.inspect(), 3000);
  } catch (error) {
    console.error(`[docker] inspect failed for ${summary.Id}:`, error.message);
  }

  let stats = null;
  try {
    stats = await withTimeout(container.stats({ stream: false }), 3000);
  } catch (error) {
    console.error(`[docker] stats failed for ${summary.Id}:`, error.message);
  }

  const name = cleanName((summary.Names || [])[0]) || summary.Id.slice(0, 12);
  const state = (inspect && inspect.State && inspect.State.Status) || summary.State || 'unknown';
  const health =
    (inspect && inspect.State && inspect.State.Health && inspect.State.Health.Status) || 'none';

  const cpuPct = computeCpuPercent(stats);
  const memBytes = computeMemoryUsage(stats);
  const memLimit = computeMemoryLimit(stats);
  const memPct = memBytes && memLimit ? (memBytes / memLimit) * 100 : null;

  return {
    id: summary.Id,
    shortId: summary.Id.slice(0, 12),
    name,
    names: (summary.Names || []).map(cleanName),
    image: summary.Image || null,
    state,
    statusText: summary.Status || state,
    health,
    restarting: Boolean((inspect && inspect.State && inspect.State.Restarting) || state === 'restarting'),
    restartCount:
      inspect && inspect.State && Number.isFinite(Number(inspect.State.RestartCount))
        ? Number(inspect.State.RestartCount)
        : 0,
    startedAt: inspect && inspect.State ? inspect.State.StartedAt : null,
    finishedAt: inspect && inspect.State ? inspect.State.FinishedAt : null,
    labels: (inspect && inspect.Config && inspect.Config.Labels) || summary.Labels || {},
    cpuPct,
    memBytes,
    memLimit,
    memPct,
    fetchedAt: new Date().toISOString()
  };
}

async function getContainerLogs(docker, containerId, options = {}) {
  const tail = options.tail || 200;
  const since = options.since || 0;

  const container = docker.getContainer(containerId);
  const result = await container.logs({
    stdout: true,
    stderr: true,
    timestamps: true,
    tail,
    since
  });

  if (Buffer.isBuffer(result)) {
    return decodeDockerLogBuffer(result)
      .split('\n')
      .map((line) => line.trimEnd())
      .filter(Boolean);
  }

  return String(result || '')
    .split('\n')
    .map((line) => line.trimEnd())
    .filter(Boolean);
}

async function restartContainer(docker, containerId, options = {}) {
  const timeoutSec = Number.isFinite(Number(options.timeoutSec))
    ? Math.max(1, Math.floor(Number(options.timeoutSec)))
    : 10;
  const container = docker.getContainer(containerId);
  await container.restart({ t: timeoutSec });
}

async function startContainer(docker, containerId) {
  const container = docker.getContainer(containerId);
  await container.start();
}

function findContainerByNameOrId(containers, target) {
  if (!target) {
    return null;
  }

  const needle = String(target).trim().toLowerCase();
  if (!needle) {
    return null;
  }

  const exact = containers.find((container) =>
    container.id.toLowerCase().startsWith(needle) ||
    container.name.toLowerCase() === needle ||
    container.names.some((name) => name.toLowerCase() === needle)
  );

  if (exact) {
    return exact;
  }

  return containers.find((container) =>
    container.name.toLowerCase().includes(needle) ||
    String(container.image || '').toLowerCase().includes(needle)
  );
}

function decodeDockerLogBuffer(buffer) {
  let offset = 0;
  let decoded = '';

  while (offset + 8 <= buffer.length) {
    const payloadLength = buffer.readUInt32BE(offset + 4);

    if (payloadLength < 0 || offset + 8 + payloadLength > buffer.length) {
      return buffer.toString('utf8');
    }

    decoded += buffer.slice(offset + 8, offset + 8 + payloadLength).toString('utf8');
    offset += 8 + payloadLength;
  }

  if (!decoded) {
    return buffer.toString('utf8');
  }

  return decoded;
}

function computeCpuPercent(stats) {
  if (!stats || !stats.cpu_stats || !stats.precpu_stats) {
    return null;
  }

  const totalUsage = stats.cpu_stats.cpu_usage ? stats.cpu_stats.cpu_usage.total_usage : 0;
  const prevTotalUsage =
    stats.precpu_stats.cpu_usage && stats.precpu_stats.cpu_usage.total_usage
      ? stats.precpu_stats.cpu_usage.total_usage
      : 0;

  const systemUsage = stats.cpu_stats.system_cpu_usage || 0;
  const prevSystemUsage = stats.precpu_stats.system_cpu_usage || 0;

  const cpuDelta = totalUsage - prevTotalUsage;
  const systemDelta = systemUsage - prevSystemUsage;
  const onlineCpus = stats.cpu_stats.online_cpus ||
    ((stats.cpu_stats.cpu_usage && stats.cpu_stats.cpu_usage.percpu_usage
      ? stats.cpu_stats.cpu_usage.percpu_usage.length
      : 1));

  if (cpuDelta <= 0 || systemDelta <= 0 || onlineCpus <= 0) {
    return 0;
  }

  return Number(((cpuDelta / systemDelta) * onlineCpus * 100).toFixed(2));
}

function computeMemoryUsage(stats) {
  if (!stats || !stats.memory_stats) {
    return null;
  }

  const usage = stats.memory_stats.usage || 0;
  const inactiveFile =
    stats.memory_stats.stats && typeof stats.memory_stats.stats.inactive_file === 'number'
      ? stats.memory_stats.stats.inactive_file
      : 0;

  const adjusted = usage - inactiveFile;
  return adjusted > 0 ? adjusted : usage;
}

function computeMemoryLimit(stats) {
  if (!stats || !stats.memory_stats) {
    return null;
  }

  const limit = stats.memory_stats.limit || 0;
  return limit > 0 ? limit : null;
}

function cleanName(name) {
  return String(name || '').replace(/^\//, '');
}

async function withTimeout(promise, ms) {
  let timeoutId;
  const timeout = new Promise((_, reject) => {
    timeoutId = setTimeout(() => reject(new Error(`timeout after ${ms}ms`)), ms);
  });

  try {
    return await Promise.race([promise, timeout]);
  } finally {
    clearTimeout(timeoutId);
  }
}

module.exports = {
  createDockerClient,
  listContainersWithStats,
  getContainerLogs,
  restartContainer,
  startContainer,
  findContainerByNameOrId
};
