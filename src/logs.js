function filterLogs(input, options = {}) {
  const maxLines = options.maxLines || 120;
  const maxChars = options.maxChars || 240;

  const lines = Array.isArray(input)
    ? input
    : String(input || '')
        .split('\n')
        .map((line) => line.trimEnd());

  const cleaned = lines
    .map((line) => truncateLine(String(line || '').trim(), maxChars))
    .filter(Boolean);

  const deduped = [];
  const seen = new Set();

  for (const line of cleaned) {
    const key = normalizeForDedup(line);
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    deduped.push(line);
  }

  const scored = deduped.map((line, index) => ({
    line,
    score: scoreLine(line),
    index
  }));

  scored.sort((a, b) => {
    if (b.score !== a.score) {
      return b.score - a.score;
    }
    return b.index - a.index;
  });

  const selected = scored.slice(0, maxLines).map((item) => item.line);

  return {
    lines: selected,
    stats: {
      inputLines: lines.length,
      dedupedLines: deduped.length,
      selectedLines: selected.length,
      errorLines: scored.filter((item) => item.score >= 8).length,
      warningLines: scored.filter((item) => item.score >= 5 && item.score < 8).length
    }
  };
}

function scoreLine(line) {
  const text = line.toLowerCase();

  if (/(fatal|panic|segfault|out of memory|oom|unhandled exception)/.test(text)) {
    return 10;
  }

  if (/(error|exception|failed|failure|refused|cannot|timed out|timeout)/.test(text)) {
    return 8;
  }

  if (/(warn|warning|retry|degraded|slow)/.test(text)) {
    return 5;
  }

  return 1;
}

function normalizeForDedup(line) {
  return line
    .toLowerCase()
    .replace(/\d{4}-\d{2}-\d{2}t\d{2}:\d{2}:\d{2}(\.\d+)?z/g, '')
    .replace(/\b\d{2}:\d{2}:\d{2}(\.\d+)?\b/g, '')
    .replace(/\b[0-9a-f]{8,}\b/g, '#')
    .replace(/\s+/g, ' ')
    .trim();
}

function truncateLine(line, maxChars) {
  if (line.length <= maxChars) {
    return line;
  }
  return `${line.slice(0, Math.max(0, maxChars - 3))}...`;
}

module.exports = {
  filterLogs
};
