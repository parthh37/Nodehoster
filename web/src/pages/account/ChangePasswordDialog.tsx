import { useEffect, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { KeyRound } from 'lucide-react';
import { authApi } from '@/api/endpoints';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { ErrorBox, Field } from '@/components/Field';
import { Input } from '@/components/Input';
import { useToast } from '@/components/Toast';
import { errorMessage } from '@/api/client';

export function ChangePasswordDialog({ open, onClose, forced }: { open: boolean; onClose: () => void; forced?: boolean }) {
  const toast = useToast();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (open) {
      setCurrent('');
      setNext('');
      setConfirm('');
      setTouched(false);
    }
  }, [open]);

  const m = useMutation({
    mutationFn: () => authApi.changePassword(current, next),
    onSuccess: () => {
      toast.success('Password changed');
      onClose();
    },
  });
  useEffect(() => {
    if (open) m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const mismatch = touched && confirm !== next ? 'Passwords do not match' : null;
  const tooShort = touched && next.length > 0 && next.length < 10 ? 'Use at least 10 characters' : null;
  const same = touched && next && next === current ? 'Choose a password different from the current one' : null;

  const submit = () => {
    setTouched(true);
    if (!current || !next || next !== confirm || next.length < 10 || next === current) return;
    m.mutate();
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      dismissible={!forced}
      size="sm"
      title={forced ? 'Set a new password' : 'Change password'}
      description={
        forced
          ? 'Your password was set by an administrator or is the initial password. Choose a new one to continue.'
          : 'You stay signed in on this device.'
      }
      icon={
        <span className="flex h-8 w-8 items-center justify-center rounded-full bg-accent-100 text-accent-700 dark:bg-accent-500/15 dark:text-accent-400">
          <KeyRound className="h-4 w-4" />
        </span>
      }
      onSubmit={submit}
      footer={
        <>
          {!forced && <Button onClick={onClose}>Cancel</Button>}
          <Button type="submit" variant="primary" loading={m.isPending}>
            Change password
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {m.isError && <ErrorBox>{errorMessage(m.error)}</ErrorBox>}
        <Field label="Current password">
          <Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
        </Field>
        <Field label="New password" error={tooShort || same} hint="At least 10 characters.">
          <Input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
        </Field>
        <Field label="Confirm new password" error={mismatch}>
          <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        </Field>
      </div>
    </Dialog>
  );
}
