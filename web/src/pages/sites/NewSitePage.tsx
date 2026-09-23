import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, ArrowRight, Check, CornerUpRight, FolderOpen, Hexagon, Network, Rocket } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { ApiError } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Site, SiteType } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { Button } from '@/components/Button';
import { Card, Callout, KV, PageHeader } from '@/components/Layout';
import { Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, Textarea } from '@/components/Input';
import { Switch } from '@/components/Switch';
import { useToast } from '@/components/Toast';
import { bindingLabel } from '@/lib/bindings';
import { clone } from '@/lib/obj';
import { HOST_RE, LB_STRATEGIES, NAME_RE, newSite, SITE_TYPES, siteTypeLabel } from '@/lib/siteDefaults';
import { cn } from '@/lib/cn';
import { BindingsEditor } from './editors/BindingsEditor';
import { NodeEssentials, ProxyEssentials, RedirectEssentials, StaticEssentials } from './editors/TypeSettings';

const STEPS = ['Type', 'Essentials', 'Bindings', 'Review'] as const;

const typeIcons: Record<SiteType, typeof Hexagon> = {
  node: Hexagon,
  proxy: Network,
  static: FolderOpen,
  redirect: CornerUpRight,
};

function stepForField(field: string | undefined): number {
  if (!field) return 3;
  if (field.startsWith('bindings')) return 2;
  if (field === 'type') return 0;
  return 1;
}

/** Client-side checks for the essentials step; the server validates again. */
function essentialsError(s: Site): string | null {
  if (!NAME_RE.test(s.name)) return 'Enter a site name (letters, digits, space, . _ -).';
  if (s.type === 'node') {
    if (!s.node?.appRoot.trim()) return 'Enter the application path.';
    if (!s.node?.script && !s.node?.npmScript) return 'Enter an entry script or an npm script.';
    if ((s.node?.instances ?? 1) < 1 || (s.node?.instances ?? 1) > 64) return 'Instances must be between 1 and 64.';
  }
  if (s.type === 'proxy') {
    const ups = s.proxy?.upstreams ?? [];
    if (!ups.length || ups.some((u) => !/^https?:\/\/.+/.test(u.url))) return 'Enter at least one upstream http:// or https:// URL.';
  }
  if (s.type === 'static' && !s.static?.root.trim()) return 'Enter the root directory.';
  if (s.type === 'redirect' && !/^https?:\/\/.+/.test(s.redirect?.targetUrl ?? '')) return 'Enter a target http:// or https:// URL.';
  return null;
}

function bindingsError(s: Site): string | null {
  for (const b of s.bindings) {
    if (b.host && !HOST_RE.test(b.host)) return `"${b.host}" is not a valid host name.`;
    if (b.port < 1 || b.port > 65535) return 'Ports must be between 1 and 65535.';
    if (b.protocol === 'https' && b.certMode === 'certificate' && !b.certificateId) return 'Choose a certificate for each HTTPS binding set to use one.';
    if (b.protocol === 'https' && b.certMode === 'auto' && (!b.host || b.host.startsWith('*.'))) return 'Automatic certificates need a specific host name.';
  }
  return null;
}

export function NewSitePage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const { isAdmin } = usePermissions();
  const [step, setStep] = useState(0);
  const [site, setSite] = useState<Site>(() => newSite('node'));
  const [touched, setTouched] = useState(false);
  const [startNow, setStartNow] = useState(true);

  const update = (fn: (d: Site) => void) =>
    setSite((s) => {
      const c = clone(s);
      fn(c);
      return c;
    });

  const chooseType = (t: SiteType) => {
    if (t === site.type) return;
    const next = newSite(t);
    next.name = site.name;
    next.description = site.description;
    next.bindings = site.bindings;
    setSite(next);
  };

  const create = useMutation({
    mutationFn: async () => {
      const payload: Site = { ...site, autoStart: startNow };
      const created = await sitesApi.create(payload);
      if (startNow && created.status?.state !== 'running' && created.status?.state !== 'starting') {
        try {
          await sitesApi.action(created.id, 'start');
        } catch (e) {
          toast.error('Site created, but it could not be started', e);
        }
      }
      return created;
    },
    onSuccess: (created) => {
      void qc.invalidateQueries({ queryKey: qk.sites, exact: true });
      toast.success(`Site ${created.name} created`);
      navigate(`/sites/${created.id}`);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.field) setStep(stepForField(e.field));
    },
  });

  const clientErr = useMemo(() => {
    if (step === 1) return essentialsError(site);
    if (step === 2) return bindingsError(site);
    return null;
  }, [step, site]);

  const next = () => {
    setTouched(true);
    if (clientErr) return;
    setTouched(false);
    setStep((s) => Math.min(STEPS.length - 1, s + 1));
  };

  if (!isAdmin) {
    return (
      <Callout tone="info" title="Administrators only">
        Only administrators can create sites.
      </Callout>
    );
  }

  const Icon = typeIcons[site.type];

  return (
    <div className="mx-auto max-w-4xl">
      <Link to="/sites" className="mb-2 inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
        <ArrowLeft className="h-3 w-3" /> Sites
      </Link>
      <PageHeader title="New site" description="Add a site to this server. You can change everything later." />

      {/* Stepper */}
      <ol className="mb-5 flex items-center gap-2">
        {STEPS.map((label, i) => (
          <li key={label} className="flex flex-1 items-center gap-2">
            <button
              type="button"
              disabled={i > step}
              onClick={() => i < step && setStep(i)}
              className={cn(
                'flex items-center gap-2 text-[13px] font-medium',
                i === step ? 'text-zinc-900 dark:text-zinc-50' : i < step ? 'text-accent-700 dark:text-accent-400' : 'text-zinc-400',
              )}
            >
              <span
                className={cn(
                  'flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-xs',
                  i === step
                    ? 'border-accent-600 bg-accent-600 text-white dark:border-accent-500 dark:bg-accent-500'
                    : i < step
                      ? 'border-accent-600 text-accent-700 dark:border-accent-500 dark:text-accent-400'
                      : 'border-zinc-300 dark:border-zinc-700',
                )}
              >
                {i < step ? <Check className="h-3.5 w-3.5" /> : i + 1}
              </span>
              {label}
            </button>
            {i < STEPS.length - 1 && <span className={cn('h-px flex-1', i < step ? 'bg-accent-500' : 'bg-zinc-200 dark:bg-zinc-800')} />}
          </li>
        ))}
      </ol>

      <FormErrors error={create.error}>
        <FormErrorBanner className="mb-4" />
        {touched && clientErr && <Callout tone="danger" className="mb-4">{clientErr}</Callout>}

        {step === 0 && (
          <div className="grid gap-3 sm:grid-cols-2">
            {SITE_TYPES.map((t) => {
              const TIcon = typeIcons[t.value];
              const active = site.type === t.value;
              return (
                <button
                  key={t.value}
                  type="button"
                  onClick={() => chooseType(t.value)}
                  onDoubleClick={() => {
                    chooseType(t.value);
                    setStep(1);
                  }}
                  className={cn(
                    'nh-card flex items-start gap-3 p-4 text-left transition-colors',
                    active ? 'border-accent-600 ring-2 ring-accent-500/20 dark:border-accent-500' : 'hover:border-zinc-300 dark:hover:border-zinc-700',
                  )}
                >
                  <span
                    className={cn(
                      'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg',
                      active ? 'bg-accent-600 text-white' : 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
                    )}
                  >
                    <TIcon className="h-5 w-5" />
                  </span>
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold">{t.label}</span>
                    <span className="mt-0.5 block text-xs text-zinc-500 dark:text-zinc-400">{t.description}</span>
                  </span>
                </button>
              );
            })}
          </div>
        )}

        {step === 1 && (
          <Card title={<span className="flex items-center gap-2"><Icon className="h-4 w-4 text-zinc-400" />{siteTypeLabel(site.type)}</span>}>
            <div className="space-y-4">
              <Field label="Site name" path="name" required hint="Shown in the console. Letters, digits, spaces, '.', '_' and '-'.">
                <Input
                  autoFocus
                  value={site.name}
                  placeholder="my-api"
                  maxLength={64}
                  onChange={(e) =>
                    update((d) => {
                      d.name = e.target.value;
                    })
                  }
                />
              </Field>
              <Field label="Description" path="description">
                <Textarea
                  rows={2}
                  value={site.description ?? ''}
                  onChange={(e) =>
                    update((d) => {
                      d.description = e.target.value;
                    })
                  }
                />
              </Field>
              <div className="border-t border-zinc-200 pt-4 dark:border-zinc-800">
                {site.type === 'node' && <NodeEssentials site={site} update={update} />}
                {site.type === 'proxy' && <ProxyEssentials site={site} update={update} />}
                {site.type === 'static' && <StaticEssentials site={site} update={update} />}
                {site.type === 'redirect' && <RedirectEssentials site={site} update={update} />}
              </div>
            </div>
          </Card>
        )}

        {step === 2 && (
          <Card
            title="Bindings"
            description="How requests reach this site. A binding is a protocol, IP address, port and optional host name — just like IIS."
          >
            <BindingsEditor site={site} update={update} />
          </Card>
        )}

        {step === 3 && (
          <Card title="Review">
            <div className="space-y-5">
              <KV
                items={[
                  ['Name', <span className="font-medium">{site.name}</span>],
                  ['Type', siteTypeLabel(site.type)],
                  ...(site.description ? ([['Description', site.description]] as [string, string][]) : []),
                  ...reviewItems(site),
                  [
                    'Bindings',
                    site.bindings.length ? (
                      <div className="space-y-0.5">
                        {site.bindings.map((b, i) => (
                          <div key={i} className="font-mono text-[12.5px]">
                            {bindingLabel(b)}
                            {b.protocol === 'https' && <span className="ml-2 font-sans text-xs text-zinc-500">{b.certMode === 'auto' ? "Let's Encrypt" : 'selected certificate'}</span>}
                          </div>
                        ))}
                      </div>
                    ) : (
                      <span className="text-amber-600">None — the site will not be reachable</span>
                    ),
                  ],
                ]}
              />
              <div className="border-t border-zinc-200 pt-4 dark:border-zinc-800">
                <Switch
                  checked={startNow}
                  onChange={setStartNow}
                  label="Start the site now"
                  description="Also starts automatically with the NodeHoster service."
                />
              </div>
              {site.type === 'node' && (
                <Callout tone="info">
                  After creating, add environment variables on the <em>Environment</em> tab or set up Git / .zip deployments on the{' '}
                  <em>Deployments</em> tab.
                </Callout>
              )}
            </div>
          </Card>
        )}
      </FormErrors>

      <div className="mt-5 flex items-center justify-between">
        <Button icon={<ArrowLeft className="h-4 w-4" />} disabled={step === 0} onClick={() => setStep((s) => s - 1)}>
          Back
        </Button>
        {step < STEPS.length - 1 ? (
          <Button variant="primary" iconRight={<ArrowRight className="h-4 w-4" />} onClick={next}>
            Next
          </Button>
        ) : (
          <Button variant="primary" icon={<Rocket className="h-4 w-4" />} loading={create.isPending} onClick={() => create.mutate()}>
            Create site
          </Button>
        )}
      </div>
    </div>
  );
}

function reviewItems(s: Site): [string, React.ReactNode][] {
  const mono = (v: React.ReactNode) => <span className="font-mono text-[12.5px]">{v}</span>;
  switch (s.type) {
    case 'node':
      return [
        ['Application path', mono(s.node!.appRoot)],
        ['Start', mono(s.node!.npmScript ? `npm run ${s.node!.npmScript}` : `node ${s.node!.script}`)],
        ['Node.js', s.node!.nodeVersion ? mono(s.node!.nodeVersion) : 'Server default'],
        ['Instances', String(s.node!.instances)],
      ];
    case 'proxy':
      return [
        ['Upstreams', <div className="space-y-0.5">{s.proxy!.upstreams.map((u, i) => <div key={i}>{mono(u.url)}</div>)}</div>],
        ['Load balancing', LB_STRATEGIES.find((l) => l.value === s.proxy!.loadBalancing)?.label ?? s.proxy!.loadBalancing],
      ];
    case 'static':
      return [
        ['Root', mono(s.static!.root)],
        ['SPA fallback', s.static!.spaFallback ? 'Yes' : 'No'],
      ];
    case 'redirect':
      return [
        ['Target', mono(s.redirect!.targetUrl)],
        ['Status', String(s.redirect!.statusCode)],
        ['Preserve path', s.redirect!.preservePath ? 'Yes' : 'No'],
      ];
  }
}
