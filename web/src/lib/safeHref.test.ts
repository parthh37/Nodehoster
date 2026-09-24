import { describe, expect, it } from 'vitest';
import { safeHref } from './safeHref';

describe('safeHref', () => {
  it('keeps absolute http(s) URLs', () => {
    expect(safeHref('https://github.com/acme/shop/pull/42')).toBe('https://github.com/acme/shop/pull/42');
    expect(safeHref('http://pr-42.preview.example.com/')).toBe('http://pr-42.preview.example.com/');
    expect(safeHref('  HTTPS://Example.com:8443/a?b=1#c ')).toBe('https://example.com:8443/a?b=1#c');
  });
  it('refuses script, data and other schemes, however written', () => {
    for (const bad of [
      'javascript:alert(1)',
      ' JavaScript:alert(1)',
      'java\tscript:alert(1)',
      'javascript://x.example/%0aalert(1)',
      'data:text/html,<script>alert(1)</script>',
      'vbscript:msgbox(1)',
      'file:///C:/Windows/win.ini',
      'ftp://example.com/',
    ]) {
      expect(safeHref(bad)).toBeUndefined();
    }
  });
  it('refuses relative and empty values', () => {
    for (const bad of ['/api/servers', '//evil.example/x', 'pr-42.example.com', '', null, undefined]) {
      expect(safeHref(bad)).toBeUndefined();
    }
  });
});
