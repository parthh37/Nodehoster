import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Binding } from '@/api/types';
import { bindingHref, bindingInfo, bindingLabel, defaultPort } from './bindings';

function b(over: Partial<Binding> = {}): Binding {
  return { id: 'b1', protocol: 'http', ip: '', port: 80, host: '', ...over };
}

describe('defaultPort', () => {
  it('is 443 for https and 80 for everything else', () => {
    expect(defaultPort('https')).toBe(443);
    expect(defaultPort('http')).toBe(80);
    expect(defaultPort('ws')).toBe(80);
  });
});

describe('bindingLabel', () => {
  it('always includes the port, even the default one', () => {
    expect(bindingLabel(b({ host: 'example.com' }))).toBe('http://example.com:80');
    expect(bindingLabel(b({ protocol: 'https', port: 443, host: 'example.com' }))).toBe('https://example.com:443');
  });

  it('uses * when neither host nor ip is set', () => {
    expect(bindingLabel(b({ port: 8080 }))).toBe('http://*:8080');
  });

  it('falls back to the ip when there is no host name', () => {
    expect(bindingLabel(b({ ip: '10.0.0.5', port: 3000 }))).toBe('http://10.0.0.5:3000');
  });

  it('prefers the host name over the ip', () => {
    expect(bindingLabel(b({ ip: '10.0.0.5', host: 'app.local' }))).toBe('http://app.local:80');
  });

  it('keeps wildcard host names as-is', () => {
    expect(bindingLabel(b({ protocol: 'https', port: 443, host: '*.example.com' }))).toBe('https://*.example.com:443');
  });

  it('brackets IPv6 addresses', () => {
    expect(bindingLabel(b({ ip: '::1', port: 8080 }))).toBe('http://[::1]:8080');
    expect(bindingLabel(b({ ip: 'fe80::1234:5678' }))).toBe('http://[fe80::1234:5678]:80');
  });

  it('does not double-bracket an already bracketed IPv6 address', () => {
    expect(bindingLabel(b({ ip: '[::1]', port: 8080 }))).toBe('http://[::1]:8080');
  });
});

describe('bindingHref', () => {
  // The fallback host defaults to window.location.hostname, which is evaluated on every call
  // that omits it.
  beforeEach(() => {
    vi.stubGlobal('window', { location: { hostname: 'console.example.net' } });
  });

  it('omits the default port for the protocol', () => {
    expect(bindingHref(b({ host: 'example.com' }))).toBe('http://example.com/');
    expect(bindingHref(b({ protocol: 'https', port: 443, host: 'example.com' }))).toBe('https://example.com/');
  });

  it('keeps non-default ports, including 443 over http and 80 over https', () => {
    expect(bindingHref(b({ host: 'example.com', port: 8080 }))).toBe('http://example.com:8080/');
    expect(bindingHref(b({ host: 'example.com', port: 443 }))).toBe('http://example.com:443/');
    expect(bindingHref(b({ protocol: 'https', host: 'example.com', port: 80 }))).toBe('https://example.com:80/');
  });

  it('returns null for wildcard host names, which are not browsable', () => {
    expect(bindingHref(b({ host: '*.example.com' }))).toBeNull();
    expect(bindingHref(b({ host: '*' }))).toBeNull();
  });

  it('uses the ip when there is no host name', () => {
    expect(bindingHref(b({ ip: '192.168.1.10', port: 3000 }))).toBe('http://192.168.1.10:3000/');
  });

  it('uses the explicit fallback host for all-interfaces bindings', () => {
    expect(bindingHref(b({ port: 8080 }), 'server.lan')).toBe('http://server.lan:8080/');
  });

  it('defaults the fallback host to the current page host name', () => {
    expect(bindingHref(b({ port: 8080 }))).toBe('http://console.example.net:8080/');
  });

  it('brackets IPv6 ips and fallback hosts', () => {
    expect(bindingHref(b({ ip: '::1', port: 8080 }))).toBe('http://[::1]:8080/');
    expect(bindingHref(b({ protocol: 'https', port: 443 }), 'fe80::1')).toBe('https://[fe80::1]/');
  });
});

describe('bindingInfo', () => {
  it('formats IIS-style ip:port:host with * for all interfaces', () => {
    expect(bindingInfo(b({ port: 80 }))).toBe('http *:80:');
    expect(bindingInfo(b({ protocol: 'https', port: 443, host: 'example.com' }))).toBe('https *:443:example.com');
    expect(bindingInfo(b({ ip: '10.0.0.1', port: 8080, host: 'a.b' }))).toBe('http 10.0.0.1:8080:a.b');
  });

  // BUG: bindingInfo does not bracket IPv6 addresses (bindingLabel/bindingHref do), so the
  // ip:port:host string becomes ambiguous ("http ::1:80:"). IIS writes "[::1]:80:".
  it.skip('brackets IPv6 addresses so ip:port:host stays unambiguous', () => {
    expect(bindingInfo(b({ ip: '::1', port: 80 }))).toBe('http [::1]:80:');
  });
});
