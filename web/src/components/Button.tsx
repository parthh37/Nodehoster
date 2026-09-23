import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { Loader2 } from 'lucide-react';
import { cn } from '@/lib/cn';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'danger-ghost';
export type ButtonSize = 'xs' | 'sm' | 'md';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  icon?: ReactNode;
  iconRight?: ReactNode;
}

const variants: Record<ButtonVariant, string> = {
  primary:
    'bg-accent-700 text-white shadow-sm hover:bg-accent-800 active:bg-accent-900 dark:bg-accent-600 dark:hover:bg-accent-500 dark:active:bg-accent-700 border border-transparent',
  secondary:
    'border border-zinc-300 bg-white text-zinc-800 shadow-sm hover:bg-zinc-50 active:bg-zinc-100 dark:border-zinc-700 dark:bg-zinc-800/80 dark:text-zinc-100 dark:hover:bg-zinc-800 dark:active:bg-zinc-700',
  ghost:
    'border border-transparent text-zinc-700 hover:bg-zinc-100 active:bg-zinc-200 dark:text-zinc-300 dark:hover:bg-zinc-800 dark:active:bg-zinc-700',
  danger:
    'border border-transparent bg-red-600 text-white shadow-sm hover:bg-red-700 active:bg-red-800 dark:bg-red-600 dark:hover:bg-red-500',
  'danger-ghost':
    'border border-transparent text-red-600 hover:bg-red-50 active:bg-red-100 dark:text-red-400 dark:hover:bg-red-500/10',
};

const sizes: Record<ButtonSize, string> = {
  xs: 'h-6 text-xs gap-1 rounded',
  sm: 'h-7 text-xs gap-1.5 rounded-md',
  md: 'h-8 text-sm gap-2 rounded-md',
};

const padding: Record<ButtonSize, string> = { xs: 'px-1.5', sm: 'px-2.5', md: 'px-3' };
const square: Record<ButtonSize, string> = { xs: 'w-6', sm: 'w-7', md: 'w-8' };

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', loading, icon, iconRight, className, children, disabled, type = 'button', ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      className={cn(
        'inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap font-medium transition-colors',
        'disabled:pointer-events-none disabled:opacity-50',
        variants[variant],
        sizes[size],
        children || iconRight ? padding[size] : square[size],
        '[&>svg]:shrink-0',
        className,
      )}
      {...rest}
    >
      {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : icon}
      {children}
      {iconRight}
    </button>
  );
});

/** Square icon button with an accessible label and tooltip. */
export function IconButton({
  label,
  icon,
  variant = 'ghost',
  size = 'sm',
  ...rest
}: Omit<ButtonProps, 'children'> & { label: string; icon: ReactNode }) {
  return <Button aria-label={label} title={label} variant={variant} size={size} icon={icon} {...rest} />;
}
