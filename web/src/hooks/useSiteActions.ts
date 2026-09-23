import { useMutation, useQueryClient } from '@tanstack/react-query';
import { sitesApi, type SiteAction } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';

const verbs: Record<SiteAction, [string, string]> = {
  start: ['Starting', 'started'],
  stop: ['Stopping', 'stopped'],
  restart: ['Restarting', 'restarted'],
  recycle: ['Recycling', 'recycled'],
};

/** Start/stop/restart/recycle with confirmation for disruptive actions and toasts. */
export function useSiteActions() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();

  const m = useMutation({
    mutationFn: ({ id, action }: { id: string; name: string; action: SiteAction }) => sitesApi.action(id, action),
    onSuccess: (_s, { id, name, action }) => {
      toast.success(`${name} ${verbs[action][1]}`);
      void qc.invalidateQueries({ queryKey: qk.site(id) });
      void qc.invalidateQueries({ queryKey: qk.sites, exact: true });
    },
    onError: (e, { name, action }) => toast.error(`Could not ${action} ${name}`, e),
  });

  const run = async (id: string, name: string, action: SiteAction) => {
    if (action === 'stop') {
      const r = await confirm({
        title: `Stop ${name}?`,
        message: 'The site stops answering requests on its bindings until it is started again.',
        confirmLabel: 'Stop site',
        danger: true,
      });
      if (!r.ok) return;
    }
    if (action === 'restart') {
      const r = await confirm({
        title: `Restart ${name}?`,
        message: 'A hard restart stops every instance before starting them again, so requests fail briefly. Use Recycle for a zero-downtime rolling restart.',
        confirmLabel: 'Restart',
        danger: true,
      });
      if (!r.ok) return;
    }
    m.mutate({ id, name, action });
  };

  return { run, pending: m.isPending ? m.variables : undefined };
}
