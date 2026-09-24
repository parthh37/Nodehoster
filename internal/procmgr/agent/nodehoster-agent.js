/*
 * NodeHoster agent. Preloaded into hosted Node.js applications with
 * `--require` when "agent" is enabled for a site, and into Bun applications
 * started from an entry script (Bun runs it through its Node.js
 * compatibility: net, process events and memoryUsage behave the same).
 *
 * Windows has no SIGTERM, so a supervisor cannot ask a Node process to shut
 * down gracefully. The agent fills that gap: it connects back to NodeHoster
 * over a named pipe, and when asked to stop it
 *   1. emits SIGTERM / SIGINT on `process` if the app listens for them, and a
 *      pm2-compatible `process.emit('message', 'shutdown')`, otherwise
 *   2. stops every HTTP/TCP server from accepting connections and exits once
 *      in-flight requests have finished.
 * It also reports heap usage and event-loop lag for the dashboard.
 *
 * Everything is unref'd: the agent never keeps a process alive, and any
 * failure inside it is swallowed so it can never take the application down.
 */
'use strict';

(function nodehosterAgent() {
  const pipePath = process.env.NODEHOSTER_AGENT_PIPE;
  const token = process.env.NODEHOSTER_AGENT_TOKEN;
  if (!pipePath || !token) return;

  // NODE_OPTIONS reaches npm itself when a site runs `npm run <script>`.
  // Let npm (and npx) pass through; the application it starts is the one
  // that should report.
  const main = String(process.argv[1] || '').replace(/\\/g, '/');
  if (/\/(npm|npx)(-cli)?(\.js)?$|\/npm\/bin\//.test(main)) return;

  // Children of the application (cluster workers, child_process) must not
  // claim to be the instance.
  delete process.env.NODEHOSTER_AGENT_TOKEN;

  let net, perfHooks;
  try {
    net = require('net');
    perfHooks = require('perf_hooks');
  } catch (_) {
    return;
  }

  const servers = new Set();
  function patchListen(proto) {
    const origListen = proto.listen;
    proto.listen = function patchedListen() {
      servers.add(this);
      this.once('close', () => servers.delete(this));
      this.once('listening', () => {
        try {
          const a = this.address();
          send({ type: 'listening', port: a && typeof a === 'object' ? a.port : 0 });
        } catch (_) { /* ignore */ }
      });
      return origListen.apply(this, arguments);
    };
  }
  try {
    patchListen(net.Server.prototype);
    // Under Bun, http.Server does not inherit net.Server's listen (under
    // Node.js it does, and loading https here would only slow startup).
    if (process.versions && process.versions.bun) {
      for (const mod of ['http', 'https']) {
        const proto = require(mod).Server.prototype;
        if (proto.listen !== net.Server.prototype.listen) patchListen(proto);
      }
    }
  } catch (_) { /* ignore */ }

  let socket = null;
  let buffer = '';
  let shuttingDown = false;

  function send(msg) {
    if (!socket || socket.destroyed) return;
    try {
      socket.write(JSON.stringify(msg) + '\n');
    } catch (_) { /* ignore */ }
  }

  function gracefulShutdown(timeoutMs) {
    if (shuttingDown) return;
    shuttingDown = true;

    const hasSignalHandlers =
      process.listenerCount('SIGTERM') > 0 || process.listenerCount('SIGINT') > 0;

    if (process.listenerCount('message') > 0) {
      try { process.emit('message', 'shutdown'); } catch (_) { /* ignore */ }
    }
    if (process.listenerCount('SIGTERM') > 0) {
      try { process.emit('SIGTERM', 'SIGTERM'); } catch (_) { /* ignore */ }
    } else if (process.listenerCount('SIGINT') > 0) {
      try { process.emit('SIGINT', 'SIGINT'); } catch (_) { /* ignore */ }
    }
    if (hasSignalHandlers) return; // the application owns its shutdown now

    // No handlers: close listeners and let in-flight requests finish.
    let pending = servers.size;
    const done = () => process.exit(0);
    if (pending === 0) return done();
    for (const s of servers) {
      try {
        s.close(() => { if (--pending === 0) done(); });
        if (typeof s.closeIdleConnections === 'function') s.closeIdleConnections();
      } catch (_) {
        if (--pending === 0) done();
      }
    }
    const t = setTimeout(done, Math.max(1000, (timeoutMs || 10000) - 1000));
    if (t.unref) t.unref();
  }

  function handle(line) {
    let msg;
    try { msg = JSON.parse(line); } catch (_) { return; }
    if (msg && msg.type === 'shutdown') gracefulShutdown(msg.timeoutMs);
  }

  const LOOP_RESOLUTION_MS = 20;
  let loopDelay = null;
  try {
    loopDelay = perfHooks.monitorEventLoopDelay({ resolution: LOOP_RESOLUTION_MS });
    loopDelay.enable();
  } catch (_) { /* older Node */ }

  function report() {
    try {
      const m = process.memoryUsage();
      let lag = 0;
      if (loopDelay) {
        // The histogram's samples include its own sampling interval.
        lag = Math.max(0, loopDelay.mean / 1e6 - LOOP_RESOLUTION_MS);
        if (!isFinite(lag)) lag = 0;
        loopDelay.reset();
      }
      send({ type: 'stats', heapUsed: m.heapUsed, heapTotal: m.heapTotal, rss: m.rss, lag });
    } catch (_) { /* ignore */ }
  }

  function connect(attempt) {
    try {
      socket = net.connect(pipePath);
    } catch (_) {
      return;
    }
    socket.setEncoding('utf8');
    socket.on('connect', () => {
      send({ type: 'hello', token, pid: process.pid, node: process.version, bun: (process.versions && process.versions.bun) || undefined });
      report();
    });
    socket.on('data', (chunk) => {
      buffer += chunk;
      let i;
      while ((i = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, i);
        buffer = buffer.slice(i + 1);
        if (line) handle(line);
      }
    });
    socket.on('error', () => { /* reconnect on close */ });
    socket.on('close', () => {
      socket = null;
      if (shuttingDown || attempt > 20) return;
      const t = setTimeout(() => connect(attempt + 1), Math.min(30000, 1000 * (attempt + 1)));
      if (t.unref) t.unref();
    });
    socket.unref();
  }

  connect(0);
  const timer = setInterval(report, 5000);
  if (timer.unref) timer.unref();
})();
