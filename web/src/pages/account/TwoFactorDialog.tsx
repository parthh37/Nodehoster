import { useEffect, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import QRCode from 'qrcode';
import { ShieldCheck } from 'lucide-react';
import { authApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { TOTPSetup } from '@/api/types';
import { useMe } from '@/hooks/useAuth';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { ErrorBox, Field } from '@/components/Field';
import { Input } from '@/components/Input';
import { Callout, Spinner } from '@/components/Layout';
import { CopyField } from '@/components/CopyButton';
import { useToast } from '@/components/Toast';

export function TwoFactorDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const me = useMe();
  const enabled = !!me.data?.user.totpEnabled;
  const qc = useQueryClient();
  const toast = useToast();
  const [setup, setSetup] = useState<TOTPSetup | null>(null);
  const [qr, setQr] = useState<string>('');
  const [code, setCode] = useState('');

  const setupM = useMutation({
    mutationFn: authApi.totpSetup,
    onSuccess: (s) => setSetup(s),
  });
  const enableM = useMutation({
    mutationFn: () => authApi.totpEnable(code.trim()),
    onSuccess: () => {
      toast.success('Two-factor authentication enabled');
      void qc.invalidateQueries({ queryKey: qk.me });
      onClose();
    },
  });
  const disableM = useMutation({
    mutationFn: () => authApi.totpDisable(code.trim()),
    onSuccess: () => {
      toast.success('Two-factor authentication disabled');
      void qc.invalidateQueries({ queryKey: qk.me });
      onClose();
    },
  });

  useEffect(() => {
    if (!open) return;
    setCode('');
    setSetup(null);
    setQr('');
    setupM.reset();
    enableM.reset();
    disableM.reset();
    if (!enabled) setupM.mutate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (!setup?.url) return;
    let cancelled = false;
    QRCode.toDataURL(setup.url, { margin: 1, width: 200, errorCorrectionLevel: 'M' })
      .then((d) => !cancelled && setQr(d))
      .catch(() => setQr(''));
    return () => {
      cancelled = true;
    };
  }, [setup?.url]);

  const validCode = /^\d{6}$/.test(code.trim());

  if (enabled) {
    return (
      <Dialog
        open={open}
        onClose={onClose}
        size="sm"
        title="Two-factor authentication"
        description="2FA is enabled for your account."
        icon={<ShieldCheck className="h-5 w-5 text-emerald-600" />}
        onSubmit={() => validCode && disableM.mutate()}
        footer={
          <>
            <Button onClick={onClose}>Close</Button>
            <Button type="submit" variant="danger" disabled={!validCode} loading={disableM.isPending}>
              Disable 2FA
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          {disableM.isError && <ErrorBox>{errorMessage(disableM.error)}</ErrorBox>}
          <p className="text-[13px] text-zinc-600 dark:text-zinc-300">
            To turn off two-factor authentication, enter a current code from your authenticator app.
          </p>
          <Field label="Authentication code">
            <Input
              mono
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              placeholder="123456"
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            />
          </Field>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Set up two-factor authentication"
      description="Scan the QR code with an authenticator app (Microsoft Authenticator, Google Authenticator, 1Password…), then enter the 6-digit code."
      icon={<ShieldCheck className="h-5 w-5 text-accent-600" />}
      onSubmit={() => validCode && enableM.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!validCode || !setup} loading={enableM.isPending}>
            Enable 2FA
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {setupM.isError && <ErrorBox>{errorMessage(setupM.error)}</ErrorBox>}
        {enableM.isError && <ErrorBox>{errorMessage(enableM.error)}</ErrorBox>}
        {!setup && setupM.isPending && (
          <div className="flex h-48 items-center justify-center">
            <Spinner />
          </div>
        )}
        {setup && (
          <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
            <div className="shrink-0 rounded-lg border border-zinc-200 bg-white p-2 dark:border-zinc-700">
              {qr ? <img src={qr} alt="QR code for your authenticator app" width={176} height={176} /> : <div className="h-44 w-44" />}
            </div>
            <div className="w-full min-w-0 space-y-3">
              <Field label="Or enter this key manually">
                <CopyField value={setup.secret} />
              </Field>
              <Field label="Code from the app">
                <Input
                  mono
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  maxLength={6}
                  placeholder="123456"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                />
              </Field>
            </div>
          </div>
        )}
        <Callout tone="info">After enabling, you will be asked for a code each time you sign in.</Callout>
      </div>
    </Dialog>
  );
}
