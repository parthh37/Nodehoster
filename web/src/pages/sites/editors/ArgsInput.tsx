import { useEffect, useState } from 'react';
import { Input } from '@/components/Input';
import { joinArgs, splitArgs } from '@/lib/obj';

/** Edits a string[] as a command-line style string. */
export function ArgsInput({ value, onChange, placeholder, disabled }: { value: string[] | undefined; onChange: (v: string[]) => void; placeholder?: string; disabled?: boolean }) {
  const [text, setText] = useState(() => joinArgs(value));
  useEffect(() => {
    if (JSON.stringify(splitArgs(text)) !== JSON.stringify(value ?? [])) setText(joinArgs(value));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);
  return (
    <Input
      mono
      value={text}
      disabled={disabled}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        onChange(splitArgs(e.target.value));
      }}
    />
  );
}
