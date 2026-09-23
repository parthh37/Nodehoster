import { useState } from 'react';
import { Check, Copy } from 'lucide-react';
import { cn } from '@/lib/cn';

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall back below */
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  return ok;
}

export function CopyButton({ text, className, label = 'Copy' }: { text: string; className?: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      title={copied ? 'Copied' : label}
      aria-label={label}
      onClick={async (e) => {
        e.stopPropagation();
        if (await copyText(text)) {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        }
      }}
      className={cn(
        'inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-zinc-400 transition-colors hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200',
        className,
      )}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-emerald-600" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  );
}

/** Read-only monospace value with a copy button. */
export function CopyField({ value, className }: { value: string; className?: string }) {
  return (
    <div
      className={cn(
        'flex items-center gap-1 rounded-md border border-zinc-200 bg-zinc-50 py-1 pl-2.5 pr-1 dark:border-zinc-700 dark:bg-zinc-800/60',
        className,
      )}
    >
      <code className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-zinc-800 dark:text-zinc-200" title={value}>
        {value}
      </code>
      <CopyButton text={value} />
    </div>
  );
}
