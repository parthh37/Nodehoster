import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, ArrowRight, Check, FileSearch, Import, RotateCcw } from 'lucide-react';
import { importApi, sitesApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { ImportApplyResult, ImportFailed, ImportPreview } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { Button } from '@/components/Button';
import { Callout, PageHeader } from '@/components/Layout';
import { ErrorBox } from '@/components/Field';
import { useToast } from '@/components/Toast';
import { cn } from '@/lib/cn';
import { pluralize } from '@/lib/format';
import { buildApplyRequest, dependencyHints, initialReview, reviewProblems, type ImportDone, type ImportReview } from '@/lib/importPlan';
import { emptySourceForm, sourceReady, SourceStep, type SourceForm } from './import/SourceStep';
import { ReviewStep } from './import/ReviewStep';
import { ResultStep } from './import/ResultStep';

const STEPS = ['Source', 'Review', 'Result'] as const;

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/**
 * Import sites from IIS (applicationHost.config, this server's IIS or one
 * iisnode web.config) or PM2. The server proposes complete drafts; nothing
 * is created until the reviewed selection is applied. After a partial apply
 * the failed items can be fixed and applied again without recreating the
 * others.
 */
export function ImportSitesPage() {
  const { isAdmin } = usePermissions();
  const qc = useQueryClient();
  const toast = useToast();
  const [step, setStep] = useState(0);
  const [form, setForm] = useState<SourceForm>(emptySourceForm);
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [review, setReview] = useState<ImportReview>({});
  const [done, setDone] = useState<ImportDone>({});
  const [failed, setFailed] = useState<Record<string, ImportFailed>>({});
  const [result, setResult] = useState<ImportApplyResult | null>(null);
  const [start, setStart] = useState(false);
  const [touched, setTouched] = useState(false);

  const sitesQ = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, enabled: isAdmin });
  const sites = useMemo(() => sitesQ.data ?? [], [sitesQ.data]);

  const read = useMutation({
    mutationFn: () => {
      if (form.source === 'local-iis') return importApi.previewLocalIIS();
      const input = form.mode === 'file' ? form.file! : form.text;
      return importApi.preview(form.source, input, form.source === 'webconfig' ? { name: form.name.trim(), appRoot: form.appRoot.trim() } : {});
    },
    onSuccess: (pv) => {
      setPreview(pv);
      setReview(initialReview(pv));
      setDone({});
      setFailed({});
      setResult(null);
      setTouched(false);
      setStep(1);
    },
  });

  const apply = useMutation({
    mutationFn: () => importApi.apply(buildApplyRequest(preview!, review, start, done)),
    onSuccess: (res) => {
      const next = { ...done };
      for (const c of res.created ?? []) next[c.key] = c;
      setDone(next);
      setFailed(Object.fromEntries((res.failed ?? []).map((f) => [f.key, f])));
      setResult(res);
      setStep(2);
      // Tasks are added to existing sites too: refresh them all.
      void qc.invalidateQueries({ queryKey: qk.sites });
      const n = res.created?.length ?? 0;
      if (n && !res.failed?.length) toast.success(`Imported ${pluralize(n, 'item')}`);
    },
  });

  const problems = useMemo(() => (preview ? reviewProblems(preview, review, done) : {}), [preview, review, done]);
  const hints = useMemo(() => (preview ? dependencyHints(preview, review, done) : {}), [preview, review, done]);
  const toImport = preview ? preview.items.filter((it) => review[it.key]?.selected && !done[it.key]).length : 0;

  const onApply = () => {
    setTouched(true);
    if (Object.keys(problems).length > 0) return;
    apply.mutate();
  };

  const reset = () => {
    setStep(0);
    setForm(emptySourceForm());
    setPreview(null);
    setResult(null);
    read.reset();
    apply.reset();
  };

  if (!isAdmin) {
    return (
      <Callout tone="info" title="Administrators only">
        Only administrators can import sites.
      </Callout>
    );
  }

  // Where the last step may be reached from the stepper.
  const reachable = (i: number) => i < step || (i === 1 && !!preview) || (i === 2 && !!result);
  const readError = read.error;
  const localUnavailable = form.source === 'local-iis' && readError instanceof ApiError && readError.field === 'source';
  const blocked = touched && Object.keys(problems).length > 0;

  return (
    <div className="mx-auto max-w-5xl">
      <Link to="/sites" className="mb-2 inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
        <ArrowLeft className="h-3 w-3" /> Sites
      </Link>
      <PageHeader
        title="Import sites"
        description="Move sites from IIS (with iisnode) or PM2 to NodeHoster. You review every site before anything is created; nothing on the old server is changed."
      />

      {/* Stepper */}
      <ol className="mb-5 flex items-center gap-2">
        {STEPS.map((label, i) => (
          <li key={label} className="flex flex-1 items-center gap-2">
            <button
              type="button"
              disabled={i === step || !reachable(i)}
              onClick={() => setStep(i)}
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

      {step === 0 && (
        <div className="space-y-4">
          {readError &&
            (localUnavailable ? (
              <Callout tone="warning" title="This server's IIS cannot be read">
                {capitalize(errorMessage(readError))}. To import from another machine, choose <em>IIS (applicationHost.config)</em> and upload its
                applicationHost.config.
              </Callout>
            ) : (
              <ErrorBox>
                {readError instanceof ApiError && readError.field === 'file' ? 'The configuration could not be read: ' : ''}
                {errorMessage(readError)}
              </ErrorBox>
            ))}
          <SourceStep
            form={form}
            onChange={(f) => {
              setForm(f);
              read.reset();
            }}
          />
        </div>
      )}

      {step === 1 && preview && (
        <div className="space-y-4">
          {apply.isError && <ErrorBox>{errorMessage(apply.error)}</ErrorBox>}
          {blocked && <Callout tone="danger">Some selected items are missing a name, folder or site to run on; they are marked below.</Callout>}
          <ReviewStep
            preview={preview}
            review={review}
            onReview={setReview}
            done={done}
            failed={failed}
            problems={touched ? problems : {}}
            hints={hints}
            sites={sites}
            start={start}
            onStart={setStart}
          />
        </div>
      )}

      {step === 2 && result && <ResultStep result={result} sites={sites} started={start} />}

      <div className="mt-5 flex items-center justify-between">
        {step === 0 ? (
          <span />
        ) : step === 1 ? (
          <Button icon={<ArrowLeft className="h-4 w-4" />} onClick={() => setStep(0)}>
            Back
          </Button>
        ) : (
          <Button icon={<RotateCcw className="h-4 w-4" />} onClick={reset}>
            Import something else
          </Button>
        )}
        {step === 0 && (
          <Button
            variant="primary"
            icon={<FileSearch className="h-4 w-4" />}
            iconRight={<ArrowRight className="h-4 w-4" />}
            disabled={!sourceReady(form)}
            loading={read.isPending}
            onClick={() => read.mutate()}
          >
            {form.source === 'local-iis' ? "Read this server's IIS" : 'Read configuration'}
          </Button>
        )}
        {step === 1 && (
          <Button variant="primary" icon={<Import className="h-4 w-4" />} disabled={toImport === 0} loading={apply.isPending} onClick={onApply}>
            {toImport === 0 ? 'Nothing selected' : `Import ${pluralize(toImport, 'item')}`}
          </Button>
        )}
        {step === 2 &&
          (result?.failed?.length ? (
            <Button variant="primary" icon={<ArrowLeft className="h-4 w-4" />} onClick={() => setStep(1)}>
              Back to review
            </Button>
          ) : (
            <Link to="/sites">
              <Button variant="primary">Go to sites</Button>
            </Link>
          ))}
      </div>
    </div>
  );
}
