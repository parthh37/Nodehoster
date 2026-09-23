import { cn } from '@/lib/cn';

export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={cn('shrink-0', className)} aria-hidden>
      <rect width="32" height="32" rx="7" className="fill-accent-700 dark:fill-accent-600" />
      <path d="M9 22V10l14 12V10" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
