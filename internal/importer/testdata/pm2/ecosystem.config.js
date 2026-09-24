// PM2 ecosystem of the Contoso server.
'use strict';

/* Deployed by hand; see README. */
module.exports = {
  apps: [
    {
      name: 'api',
      script: './dist/server.js',
      cwd: 'C:\\apps\\contoso',
      instances: 'max',
      exec_mode: 'cluster',
      node_args: '--max-old-space-size=512',
      max_memory_restart: '300M',
      cron_restart: '0 3 * * *',
      watch: true,
      ignore_watch: ['uploads', "tmp"],
      kill_timeout: 8000,
      max_restarts: 10,
      min_uptime: '10s',
      env: {
        NODE_ENV: 'development',
        PORT: 3000,
        LOG_LEVEL: `info`,
      },
      env_production: {
        NODE_ENV: 'production',
        STRIPE_SECRET_KEY: "sk_live_\u0041bc",
      },
      env_staging: { NODE_ENV: 'staging' },
    },
    {
      name: 'queue-worker',
      script: 'worker.js',
      cwd: 'C:\\apps\\contoso',
      instances: 2,
      env: { REDIS_URL: 'redis://localhost:6379' },
    },
    {
      name: 'cleanup',
      script: 'jobs/cleanup.js',
      cwd: 'C:\\apps\\contoso',
      autorestart: false,
      cron_restart: '*/30 * * * *',
    },
    {
      name: 'reports',
      script: 'report.py',
      interpreter: 'python3',
      cwd: 'C:\\apps\\reports',
    },
  ],
};
