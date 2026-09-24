// Client-side mirror of the server's settings defaults (model.MailSettings.ApplyDefaults
// and friends) so forms bind safely to settings saved by older versions.

import type { IPBanSettings, MailSettings, MimeSettings, Settings, SSOSettings } from '@/api/types';
import { normalizeBackup } from './backup';
import { normalizeLogShipping } from './logShipping';
import { normalizeUpdates } from './updates';
import { normalizeAlerts } from './alerts';

export function defaultMail(): MailSettings {
  return {
    enabled: false,
    listenIp: '',
    port: 25,
    hostname: '',
    allowIps: ['127.0.0.1', '::1'],
    requireAuth: false,
    users: [],
    certificateId: '',
    allowedSenderDomains: [],
    maxMessageMB: 25,
    maxRecipients: 100,
    delivery: 'direct',
    smartHost: { host: '', port: 587, security: 'starttls', username: '', password: '', insecureSkipVerify: false },
    expireHours: 48,
    keepFailedDays: 7,
    dkim: [],
    pickupDirectory: false,
  };
}

/** Fills zero values the way the server does. Does not modify the input. */
export function normalizeMail(input: Partial<MailSettings> | null | undefined): MailSettings {
  const d = defaultMail();
  const m = input ?? {};
  const sh = { ...d.smartHost, ...m.smartHost };
  return {
    ...d,
    ...m,
    port: m.port || d.port,
    listenIp: m.listenIp === '*' ? '' : (m.listenIp ?? ''),
    allowIps: m.allowIps ?? d.allowIps,
    users: (m.users ?? []).map((u) => ({ ...u })),
    allowedSenderDomains: m.allowedSenderDomains ?? [],
    maxMessageMB: m.maxMessageMB && m.maxMessageMB > 0 ? m.maxMessageMB : d.maxMessageMB,
    maxRecipients: m.maxRecipients && m.maxRecipients > 0 ? m.maxRecipients : d.maxRecipients,
    delivery: m.delivery || d.delivery,
    smartHost: { ...sh, port: sh.port || d.smartHost.port, security: sh.security || d.smartHost.security },
    expireHours: m.expireHours && m.expireHours > 0 ? m.expireHours : d.expireHours,
    keepFailedDays: m.keepFailedDays && m.keepFailedDays > 0 ? m.keepFailedDays : d.keepFailedDays,
    dkim: (m.dkim ?? []).map((k) => ({ ...k })),
  };
}

export function normalizeMime(m: Partial<MimeSettings> | null | undefined): MimeSettings {
  return { types: m?.types ?? [], unknownTypes: m?.unknownTypes || 'serve' };
}

/** Single sign-on settings as the server fills them (model.SSOSettings.Normalize). */
export function normalizeSSO(s: Partial<SSOSettings> | null | undefined): SSOSettings {
  const v = s ?? {};
  return {
    enabled: !!v.enabled,
    label: v.label ?? '',
    issuer: v.issuer ?? '',
    clientId: v.clientId ?? '',
    clientSecret: v.clientSecret ?? '',
    scopes: v.scopes ?? [],
    usernameClaim: v.usernameClaim || 'preferred_username',
    disablePassword: !!v.disablePassword,
    autoCreate: !!v.autoCreate,
    defaultRole: v.defaultRole ?? '',
    roleClaim: v.roleClaim ?? '',
    roleMap: (v.roleMap ?? []).map((r) => ({ ...r })),
  };
}

/** Null slices and missing sections become empty values so the settings forms can bind. */
export function normalizeSettings(s: Settings): Settings {
  return {
    ...s,
    dnsProviders: s.dnsProviders ?? [],
    webhooks: s.webhooks ?? [],
    proxy: { ...s.proxy, trustedProxies: s.proxy?.trustedProxies ?? [] },
    mime: normalizeMime(s.mime),
    mail: normalizeMail(s.mail),
    sso: normalizeSSO(s.sso),
    ipBan: normalizeIPBan(s.ipBan),
    backup: normalizeBackup(s.backup),
    logShipping: normalizeLogShipping(s.logShipping),
    updates: normalizeUpdates(s.updates),
    alerts: normalizeAlerts(s.alerts),
  };
}

/** Mirror of model.DefaultIPBan. */
export function defaultIPBan(): IPBanSettings {
  return {
    enabled: false,
    authFailures: { threshold: 10, windowSec: 300 },
    notFound: { threshold: 50, windowSec: 60 },
    rateLimited: { threshold: 30, windowSec: 60 },
    trapPaths: ['/wp-login.php', '/xmlrpc.php', '/wp-admin', '/.env', '/.git/', '/phpmyadmin', '/pma', '/cgi-bin/', '/vendor/phpunit'],
    banMinutes: 15,
    maxBanMinutes: 1440,
    allowList: [],
    ipv6Prefix: 64,
  };
}

export function normalizeIPBan(b: Partial<IPBanSettings> | null | undefined): IPBanSettings {
  const d = defaultIPBan();
  if (!b) return d;
  return {
    ...d,
    ...b,
    authFailures: { ...d.authFailures, ...b.authFailures },
    notFound: { ...d.notFound, ...b.notFound },
    rateLimited: { ...d.rateLimited, ...b.rateLimited },
    trapPaths: b.trapPaths ?? [],
    allowList: b.allowList ?? [],
  };
}

/** Where .eml files are picked up: <data>\mail\pickup. */
export function pickupPath(dataDir: string | undefined): string {
  const base = (dataDir ?? '').trim().replace(/[\\/]+$/, '');
  if (!base) return 'C:\\ProgramData\\NodeHoster\\mail\\pickup';
  const sep = base.includes('/') && !base.includes('\\') ? '/' : '\\';
  return [base, 'mail', 'pickup'].join(sep);
}

/** The address a local application should connect to. */
export function smtpClientHost(listenIp: string | undefined): string {
  const ip = (listenIp ?? '').trim();
  if (!ip || ip === '0.0.0.0' || ip === '::' || ip === '*') return '127.0.0.1';
  return ip;
}

/** A nodemailer example for sending through this server. */
export function nodemailerSnippet(m: Pick<MailSettings, 'listenIp' | 'port' | 'requireAuth' | 'certificateId' | 'hostname'>): string {
  const auth = m.requireAuth ? `\n  auth: { user: 'app', pass: process.env.SMTP_PASSWORD },` : '';
  // With STARTTLS offered, nodemailer upgrades and checks the certificate
  // against the host it connected to, which is not 127.0.0.1.
  const tls = m.certificateId ? `\n  tls: { servername: '${m.hostname || 'mail.example.com'}' },` : '';
  return `const nodemailer = require('nodemailer');

const transporter = nodemailer.createTransport({
  host: '${smtpClientHost(m.listenIp)}',
  port: ${m.port || 25},
  secure: false,${tls}${auth}
});

await transporter.sendMail({
  from: 'app@example.com',
  to: 'someone@example.org',
  subject: 'Hello from NodeHoster',
  text: 'It works.',
});`;
}
