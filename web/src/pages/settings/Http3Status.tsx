import { useQuery } from '@tanstack/react-query';
import { tlsApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import { AlertTriangle, Radio } from 'lucide-react';
import { Callout, Mono } from '@/components/Layout';

/**
 * The UDP listeners HTTP/3 opened, as saved (the switch above takes effect
 * when the settings are saved), and the firewall note.
 */
export function Http3Status({ enabled }: { enabled: boolean }) {
  const q = useQuery({ queryKey: qk.tls, queryFn: tlsApi.get, refetchInterval: 15_000 });
  const listeners = q.data?.http3Listeners ?? [];
  const failed = listeners.filter((l) => l.startsWith('FAILED'));
  if (!enabled && !q.data?.http3) return null;
  return (
    <div className="space-y-2">
      {q.data?.http3 && (
        <p className="text-xs text-zinc-500">
          {listeners.length === 0 ? (
            'No HTTPS bindings are running, so no UDP listener is open yet.'
          ) : (
            <>
              Listening on{' '}
              {listeners
                .filter((l) => !l.startsWith('FAILED'))
                .map((l, i) => (
                  <Mono key={l}>
                    {i > 0 && ', '}
                    {l}
                  </Mono>
                ))}
            </>
          )}
        </p>
      )}
      {failed.length > 0 && (
        <Callout tone="warning" icon={<AlertTriangle />} title="Some UDP ports could not be opened">
          {failed.map((f) => (
            <div key={f} className="font-mono text-xs">
              {f}
            </div>
          ))}
        </Callout>
      )}
      <Callout tone="info" icon={<Radio />} title="HTTP/3 runs over UDP">
        Browsers switch to it after a first HTTPS response (Alt-Svc). Setup’s Windows Firewall rule allows the NodeHoster program, UDP
        included; allow UDP on the HTTPS ports on any other firewall, load balancer or cloud security group in front of this server. Clients
        that cannot reach UDP keep using HTTP/2.
      </Callout>
    </div>
  );
}
