const cron = require('node-cron');

function startScheduler(options) {
  const { cronExpr, onTick } = options;
  const expression = cron.validate(cronExpr) ? cronExpr : '*/5 * * * *';

  const task = cron.schedule(expression, async () => {
    try {
      await onTick();
    } catch (error) {
      console.error('[scheduler] Scan failed:', error.message);
    }
  });

  console.log(`[scheduler] Started with cron expression: ${expression}`);
  return task;
}

module.exports = {
  startScheduler
};
