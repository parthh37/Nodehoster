import { useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, KeyRound, LogIn, ShieldCheck } from 'lucide-react';
import { authApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { Button } from '@/components/Button';
import { ErrorBox, Field } from '@/components/Field';
import { Input } from '@/components/Input';
import { Loading } from '@/components/Layout';
import { ssoErrorMessage } from '@/lib/sso';
import { Logo } from './shell/Logo';
import { useTheme } from '@/hooks/useTheme';

export function LoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const qc = useQueryClient();
  useTheme();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [needTotp, setNeedTotp] = useState(false);
  // A failed single sign-on comes back here with ?sso_error=<code>; the
  // message is ours, never text from the URL.
  const [error, setError] = useState<string | null>(() => ssoErrorMessage(params.get('sso_error')));
  const [busy, setBusy] = useState(false);
  const [usePassword, setUsePassword] = useState(false);
  const totpRef = useRef<HTMLInputElement>(null);
  const methods = useQuery({ queryKey: qk.authMethods, queryFn: authApi.methods, retry: false, staleTime: Infinity });
  // Without an answer (an older server, a network error) the password form is shown.
  const sso = methods.data?.sso ?? false;
  const passwordAllowed = methods.data?.password ?? true;
  const showPassword = passwordAllowed && (!sso || usePassword || needTotp);

  useEffect(() => {
    if (needTotp) totpRef.current?.focus();
  }, [needTotp]);

  const next = (() => {
    const n = params.get('next');
    return n && n.startsWith('/') && !n.startsWith('//') && !n.startsWith('/login') ? n : '/';
  })();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username || !password || (needTotp && !totp)) return;
    setBusy(true);
    setError(null);
    try {
      await authApi.login({ username: username.trim(), password, totp: needTotp ? totp.trim() : undefined });
      await qc.invalidateQueries({ queryKey: qk.me });
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'me' });
      navigate(next, { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.body.totpRequired === true) {
        if (needTotp && totp) setError(err.message || 'Invalid authentication code');
        setNeedTotp(true);
        setTotp('');
      } else {
        setError(err instanceof ApiError && err.status === 401 ? err.message || 'Invalid username or password' : errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-zinc-100 px-4 dark:bg-zinc-950">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center gap-3">
          <Logo className="h-10 w-10" />
          <div className="text-center">
            <h1 className="text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">NodeHoster</h1>
            <p className="text-[13px] text-zinc-500">Sign in to the server console</p>
          </div>
        </div>
        {methods.isPending ? (
          <div className="nh-card p-6 shadow-sm">
            <Loading />
          </div>
        ) : !showPassword ? (
          <div className="nh-card space-y-4 p-6 shadow-sm">
            {error && <ErrorBox>{error}</ErrorBox>}
            {sso && (
              <Button
                variant="primary"
                className="w-full"
                icon={<LogIn className="h-4 w-4" />}
                loading={busy}
                onClick={() => {
                  setBusy(true);
                  window.location.assign(authApi.ssoStartUrl(next));
                }}
              >
                {methods.data?.ssoLabel || 'Sign in with SSO'}
              </Button>
            )}
            {!sso && !passwordAllowed && <p className="text-center text-[13px] text-zinc-500">Sign-in is not available. Ask an administrator.</p>}
            {passwordAllowed && (
              <button
                type="button"
                className="flex w-full items-center justify-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
                onClick={() => {
                  setUsePassword(true);
                  setError(null);
                }}
              >
                <KeyRound className="h-3 w-3" /> Sign in with a password instead
              </button>
            )}
          </div>
        ) : (
          <form onSubmit={submit} className="nh-card space-y-4 p-6 shadow-sm" noValidate>
            {error && <ErrorBox>{error}</ErrorBox>}
            {!needTotp ? (
              <>
                <Field label="Username">
                  <Input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} spellCheck={false} />
                </Field>
                <Field label="Password">
                  <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </Field>
              </>
            ) : (
              <>
                <div className="flex items-start gap-3 rounded-md bg-zinc-50 p-3 dark:bg-zinc-800/50">
                  <ShieldCheck className="mt-0.5 h-5 w-5 shrink-0 text-accent-600" />
                  <p className="text-[13px] text-zinc-600 dark:text-zinc-300">
                    Two-factor authentication is enabled for <span className="font-medium">{username}</span>. Enter the 6-digit code from your
                    authenticator app.
                  </p>
                </div>
                <Field label="Authentication code">
                  <Input
                    ref={totpRef}
                    mono
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    maxLength={6}
                    placeholder="123456"
                    className="text-center text-lg tracking-[0.4em]"
                    value={totp}
                    onChange={(e) => setTotp(e.target.value.replace(/\D/g, ''))}
                  />
                </Field>
              </>
            )}
            <Button type="submit" variant="primary" className="w-full" loading={busy} disabled={!username || !password || (needTotp && totp.length < 6)}>
              {needTotp ? 'Verify' : 'Sign in'}
            </Button>
            {needTotp && (
              <button
                type="button"
                className="flex w-full items-center justify-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
                onClick={() => {
                  setNeedTotp(false);
                  setTotp('');
                  setError(null);
                }}
              >
                <ArrowLeft className="h-3 w-3" /> Use a different account
              </button>
            )}
            {sso && !needTotp && (
              <button
                type="button"
                className="flex w-full items-center justify-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
                onClick={() => {
                  setUsePassword(false);
                  setError(null);
                }}
              >
                <ArrowLeft className="h-3 w-3" /> {methods.data?.ssoLabel || 'Sign in with SSO'} instead
              </button>
            )}
          </form>
        )}
        {showPassword && (
          <p className="mt-6 text-center text-xs text-zinc-400">Forgot your password? Ask an administrator to reset it from the Users page.</p>
        )}
      </div>
    </div>
  );
}
