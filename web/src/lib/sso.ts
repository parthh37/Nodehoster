// Single sign-on helpers for the login page and the SSO settings tab. They
// mirror model.SSOSettings (internal/model/sso.go).

const ENTRA_HOSTS = ['login.microsoftonline.com', 'login.microsoftonline.us', 'login.partner.microsoftonline.cn', 'sts.windows.net'];

/** Whether the issuer is Microsoft Entra ID's. */
export function isEntraIssuer(issuer: string | undefined): boolean {
  try {
    return ENTRA_HOSTS.includes(new URL((issuer ?? '').trim()).hostname.toLowerCase());
  } catch {
    return false;
  }
}

/** The sign-in button text when no label is set, as the server picks it. */
export function defaultSsoLabel(issuer: string | undefined): string {
  return isEntraIssuer(issuer) ? 'Sign in with Microsoft' : 'Sign in with SSO';
}

const GUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The Entra ID issuer URL of a tenant, from its directory (tenant) ID or a
 * verified domain such as contoso.onmicrosoft.com. Returns null for input
 * that is neither, and for common/organizations/consumers, which the
 * server refuses: sign-ins must come from one tenant.
 */
export function entraIssuer(tenant: string): string | null {
  const t = tenant.trim().toLowerCase();
  if (!t || ['common', 'organizations', 'consumers'].includes(t)) return null;
  if (!GUID.test(t) && !/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(t)) return null;
  return `https://login.microsoftonline.com/${t}/v2.0`;
}

/** Messages for the ?sso_error= codes the server's callback redirects with. */
const MESSAGES: Record<string, string> = {
  off: 'Single sign-on is not enabled on this server.',
  locked: 'Too many failed sign-ins. Try again in a few minutes.',
  busy: 'Too many sign-ins are in progress. Try again in a few minutes.',
  provider: 'The identity provider could not be reached. Try again, or ask an administrator to check the single sign-on settings.',
  state: 'This sign-in expired or was already used. Start again.',
  idp: 'The identity provider did not sign you in. You may not be assigned to this application.',
  token: 'The sign-in could not be verified. Ask an administrator to check the audit log.',
  claims: 'The identity provider did not send a usable user name or groups. Ask an administrator to check the audit log.',
  unknown_user: 'Your account has no access to this server. Ask an administrator to add you.',
  no_role: 'Your account is not in a group that gives access to this server.',
  disabled: 'Your NodeHoster account is disabled.',
};

export function ssoErrorMessage(code: string | null | undefined): string | null {
  if (!code) return null;
  return MESSAGES[code] ?? 'Single sign-on failed. Ask an administrator to check the audit log.';
}
