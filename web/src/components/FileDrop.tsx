import { useRef, useState, type ReactNode } from 'react';
import { FileUp, X } from 'lucide-react';
import { cn } from '@/lib/cn';
import { formatBytes } from '@/lib/format';

export function FileDrop({
  file,
  onFile,
  accept,
  label = 'Drop a file here, or click to browse',
  hint,
  icon,
  className,
  compact,
}: {
  file: File | null;
  onFile: (f: File | null) => void;
  accept?: string;
  label?: ReactNode;
  hint?: ReactNode;
  icon?: ReactNode;
  className?: string;
  compact?: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);

  return (
    <div
      onDragOver={(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        const f = e.dataTransfer.files?.[0];
        if (f) onFile(f);
      }}
      onClick={() => input.current?.click()}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && input.current?.click()}
      className={cn(
        'flex cursor-pointer flex-col items-center justify-center rounded-lg border-2 border-dashed text-center transition-colors',
        compact ? 'px-4 py-4' : 'px-6 py-8',
        over
          ? 'border-accent-500 bg-accent-50 dark:bg-accent-500/10'
          : 'border-zinc-300 hover:border-zinc-400 hover:bg-zinc-50 dark:border-zinc-700 dark:hover:border-zinc-600 dark:hover:bg-zinc-800/40',
        className,
      )}
    >
      <input
        ref={input}
        type="file"
        accept={accept}
        className="hidden"
        onChange={(e) => {
          onFile(e.target.files?.[0] ?? null);
          e.target.value = '';
        }}
      />
      {file ? (
        <div className="flex items-center gap-2 text-[13px]">
          <FileUp className="h-4 w-4 text-accent-600" />
          <span className="font-medium text-zinc-900 dark:text-zinc-100">{file.name}</span>
          <span className="text-zinc-500">{formatBytes(file.size)}</span>
          <button
            type="button"
            className="rounded p-0.5 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200"
            onClick={(e) => {
              e.stopPropagation();
              onFile(null);
            }}
            aria-label="Remove file"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      ) : (
        <>
          <div className="mb-2 text-zinc-400 [&>svg]:h-6 [&>svg]:w-6">{icon ?? <FileUp />}</div>
          <p className="text-[13px] font-medium text-zinc-700 dark:text-zinc-300">{label}</p>
          {hint && <p className="mt-0.5 text-xs text-zinc-500">{hint}</p>}
        </>
      )}
    </div>
  );
}
