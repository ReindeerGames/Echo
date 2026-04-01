function applyGrouping(containers, config) {
  return containers.map((container) => ({
    ...container,
    group: resolveGroup(container, config)
  }));
}

function groupContainers(containers) {
  const groups = {};
  for (const container of containers) {
    const key = container.group || 'ungrouped';
    if (!groups[key]) {
      groups[key] = [];
    }
    groups[key].push(container);
  }
  return groups;
}

function resolveGroup(container, config) {
  const overrides =
    (config && config.grouping && config.grouping.overrides) || { byName: {}, byImage: {} };
  const byName = normalizeMap(overrides.byName || {});
  const byImage = normalizeMap(overrides.byImage || {});

  const name = (container.name || '').toLowerCase();
  const image = (container.image || '').toLowerCase();

  if (byName[name]) {
    return byName[name];
  }

  for (const [needle, group] of Object.entries(byImage)) {
    if (needle && image.includes(needle)) {
      return group;
    }
  }

  const composeProject =
    container.labels &&
    (container.labels['com.docker.compose.project'] ||
      container.labels['com.docker.stack.namespace']);

  if (composeProject) {
    return String(composeProject).toLowerCase();
  }

  const prefix = derivePrefix(container.name);
  if (prefix) {
    return prefix;
  }

  return 'ungrouped';
}

function derivePrefix(name) {
  const clean = String(name || '').toLowerCase().replace(/^\//, '');
  if (!clean) {
    return '';
  }

  const firstToken = clean.split(/[._-]/)[0] || clean;
  const collapsed = firstToken.replace(/\d+$/, '');
  if (collapsed.length >= 2) {
    return collapsed;
  }

  return clean.length >= 2 ? clean : '';
}

function normalizeMap(input) {
  const out = {};
  for (const [key, value] of Object.entries(input || {})) {
    out[String(key).toLowerCase()] = String(value).toLowerCase();
  }
  return out;
}

module.exports = {
  applyGrouping,
  groupContainers,
  resolveGroup
};
