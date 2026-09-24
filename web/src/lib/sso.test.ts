import { describe, expect, it } from 'vitest';
import { defaultSsoLabel, entraIssuer, isEntraIssuer, ssoErrorMessage } from './sso';

describe('isEntraIssuer', () => {
  it('recognises Entra ID hosts', () => {
    expect(isEntraIssuer('https://login.microsoftonline.com/0f1c/v2.0')).toBe(true);
    expect(isEntraIssuer('https://LOGIN.microsoftonline.us/x/v2.0')).toBe(true);
    expect(isEntraIssuer('https://sts.windows.net/0f1c/')).toBe(true);
  });
  it('rejects others and garbage', () => {
    expect(isEntraIssuer('https://accounts.google.com')).toBe(false);
    expect(isEntraIssuer('https://login.microsoftonline.com.evil.example/x')).toBe(false);
    expect(isEntraIssuer('not a url')).toBe(false);
    expect(isEntraIssuer(undefined)).toBe(false);
  });
  it('picks the default label', () => {
    expect(defaultSsoLabel('https://login.microsoftonline.com/x/v2.0')).toBe('Sign in with Microsoft');
    expect(defaultSsoLabel('https://idp.example.com')).toBe('Sign in with SSO');
  });
});

describe('entraIssuer', () => {
  it('builds the v2.0 issuer of a tenant', () => {
    expect(entraIssuer(' 72F988BF-86F1-41AF-91AB-2D7CD011DB47 ')).toBe('https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0');
    expect(entraIssuer('contoso.onmicrosoft.com')).toBe('https://login.microsoftonline.com/contoso.onmicrosoft.com/v2.0');
  });
  it('refuses multi-tenant endpoints and garbage', () => {
    expect(entraIssuer('common')).toBeNull();
    expect(entraIssuer('Organizations')).toBeNull();
    expect(entraIssuer('')).toBeNull();
    expect(entraIssuer('not/a tenant')).toBeNull();
  });
});

describe('ssoErrorMessage', () => {
  it('maps known codes', () => {
    expect(ssoErrorMessage('unknown_user')).toMatch(/no access/);
    expect(ssoErrorMessage('state')).toMatch(/expired/);
  });
  it('never echoes an unknown code', () => {
    expect(ssoErrorMessage('<b>Call 555-0100</b>')).toBe('Single sign-on failed. Ask an administrator to check the audit log.');
  });
  it('is null without a code', () => {
    expect(ssoErrorMessage(null)).toBeNull();
    expect(ssoErrorMessage('')).toBeNull();
  });
});
