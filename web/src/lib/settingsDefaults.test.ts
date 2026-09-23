import { describe, expect, it } from 'vitest';
import type { MailSettings, Settings } from '@/api/types';
import { defaultMail, nodemailerSnippet, normalizeMail, normalizeSettings, pickupPath, smtpClientHost } from './settingsDefaults';

describe('normalizeMail', () => {
  it('fills an all-zero document like the server', () => {
    expect(normalizeMail(undefined)).toEqual(defaultMail());
    const zero = {
      enabled: false,
      listenIp: '',
      port: 0,
      allowIps: null,
      requireAuth: false,
      maxMessageMB: 0,
      maxRecipients: 0,
      delivery: '',
      smartHost: { host: '', port: 0, security: '', insecureSkipVerify: false },
      expireHours: 0,
      keepFailedDays: 0,
      pickupDirectory: false,
    } as MailSettings;
    const m = normalizeMail(zero);
    expect(m.port).toBe(25);
    expect(m.allowIps).toEqual(['127.0.0.1', '::1']);
    expect(m.maxMessageMB).toBe(25);
    expect(m.maxRecipients).toBe(100);
    expect(m.delivery).toBe('direct');
    expect(m.smartHost).toMatchObject({ port: 587, security: 'starttls' });
    expect(m.expireHours).toBe(48);
    expect(m.keepFailedDays).toBe(7);
    expect(m.users).toEqual([]);
    expect(m.dkim).toEqual([]);
  });

  it('keeps an explicitly empty allow list and configured values', () => {
    const m = normalizeMail({ ...defaultMail(), allowIps: [], port: 2525, delivery: 'smarthost', listenIp: '*' });
    expect(m.allowIps).toEqual([]);
    expect(m.port).toBe(2525);
    expect(m.delivery).toBe('smarthost');
    expect(m.listenIp).toBe('');
  });

  it('does not share nested objects with the input', () => {
    const input: MailSettings = { ...defaultMail(), users: [{ username: 'a', passwordHash: '__SECRET__' }] };
    const m = normalizeMail(input);
    m.users![0].password = 'x';
    expect(input.users![0].password).toBeUndefined();
  });

  it('returns fresh defaults each call', () => {
    const a = defaultMail();
    a.allowIps!.push('10.0.0.1');
    expect(defaultMail().allowIps).toEqual(['127.0.0.1', '::1']);
  });
});

describe('normalizeSettings', () => {
  it('fills mime and mail for settings saved before they existed', () => {
    const s = normalizeSettings({ proxy: { serverHeader: 'x' }, dnsProviders: null, webhooks: null } as unknown as Settings);
    expect(s.mime).toEqual({ types: [], unknownTypes: 'serve' });
    expect(s.mail).toEqual(defaultMail());
    expect(s.dnsProviders).toEqual([]);
    expect(s.proxy.trustedProxies).toEqual([]);
  });
});

describe('pickupPath', () => {
  it('appends mail\\pickup to the data directory', () => {
    expect(pickupPath('C:\\ProgramData\\NodeHoster')).toBe('C:\\ProgramData\\NodeHoster\\mail\\pickup');
    expect(pickupPath('D:\\nh\\')).toBe('D:\\nh\\mail\\pickup');
    expect(pickupPath('/var/lib/nodehoster')).toBe('/var/lib/nodehoster/mail/pickup');
    expect(pickupPath(undefined)).toBe('C:\\ProgramData\\NodeHoster\\mail\\pickup');
  });
});

describe('nodemailer snippet', () => {
  it('connects to loopback when listening on all addresses', () => {
    expect(smtpClientHost('')).toBe('127.0.0.1');
    expect(smtpClientHost('0.0.0.0')).toBe('127.0.0.1');
    expect(smtpClientHost('10.0.0.4')).toBe('10.0.0.4');
  });

  it('includes auth and TLS server name only when needed', () => {
    const plain = nodemailerSnippet({ listenIp: '', port: 2525, requireAuth: false });
    expect(plain).toContain("host: '127.0.0.1'");
    expect(plain).toContain('port: 2525');
    expect(plain).not.toContain('auth:');
    expect(plain).not.toContain('tls:');
    const full = nodemailerSnippet({ listenIp: '', port: 25, requireAuth: true, certificateId: 'c1', hostname: 'mail.contoso.com' });
    expect(full).toContain('auth:');
    expect(full).toContain("servername: 'mail.contoso.com'");
  });
});
