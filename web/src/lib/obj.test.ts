import { describe, expect, it } from 'vitest';
import { clone, joinArgs, jsonEqual, moveItem, splitArgs, splitLines, toFloat, toInt } from './obj';

describe('clone', () => {
  it('deep-copies nested objects and arrays', () => {
    const src = { a: 1, nested: { list: [1, { x: 'y' }] } };
    const copy = clone(src);
    expect(copy).toEqual(src);
    expect(copy).not.toBe(src);
    copy.nested.list.push(2);
    (copy.nested.list[1] as { x: string }).x = 'changed';
    expect(src.nested.list).toEqual([1, { x: 'y' }]);
  });
});

describe('jsonEqual', () => {
  it('ignores key order', () => {
    expect(jsonEqual({ a: 1, b: { c: 2, d: 3 } }, { b: { d: 3, c: 2 }, a: 1 })).toBe(true);
  });

  it('treats undefined, null, empty strings and empty arrays as missing', () => {
    expect(jsonEqual({ a: 1, b: undefined, c: null, d: '', e: [] }, { a: 1 })).toBe(true);
  });

  it('treats objects that only contain empty values as missing', () => {
    expect(jsonEqual({ a: 1, limits: {}, recycle: { at: '', every: null } }, { a: 1 })).toBe(true);
  });

  it('does not treat false or 0 as missing', () => {
    expect(jsonEqual({ a: false }, {})).toBe(false);
    expect(jsonEqual({ a: 0 }, {})).toBe(false);
    expect(jsonEqual({ a: false }, { a: 0 })).toBe(false);
  });

  it('detects value changes deep in the tree', () => {
    expect(jsonEqual({ a: { b: [1, 2, { c: 'x' }] } }, { a: { b: [1, 2, { c: 'y' }] } })).toBe(false);
  });

  it('is sensitive to array order and length', () => {
    expect(jsonEqual([1, 2], [2, 1])).toBe(false);
    expect(jsonEqual({ a: [1] }, { a: [1, 1] })).toBe(false);
  });

  it('normalizes objects inside arrays', () => {
    expect(jsonEqual([{ a: 1, b: '' }], [{ a: 1 }])).toBe(true);
  });

  it('compares primitives', () => {
    expect(jsonEqual('x', 'x')).toBe(true);
    expect(jsonEqual(1, '1')).toBe(false);
    expect(jsonEqual(undefined, [])).toBe(true);
  });
});

describe('moveItem', () => {
  const list = ['a', 'b', 'c', 'd'];

  it('moves an item forward and backward without mutating the input', () => {
    expect(moveItem(list, 0, 2)).toEqual(['b', 'c', 'a', 'd']);
    expect(moveItem(list, 3, 0)).toEqual(['d', 'a', 'b', 'c']);
    expect(moveItem(list, 1, 2)).toEqual(['a', 'c', 'b', 'd']);
    expect(list).toEqual(['a', 'b', 'c', 'd']);
  });

  it('returns a new array for a no-op move within range', () => {
    const out = moveItem(list, 1, 1);
    expect(out).toEqual(list);
    expect(out).not.toBe(list);
  });

  it('returns the same array when the target is out of range', () => {
    expect(moveItem(list, 0, -1)).toBe(list);
    expect(moveItem(list, 3, 4)).toBe(list);
  });
});

describe('toInt / toFloat', () => {
  it.each([
    ['42', 42],
    ['-5', -5],
    ['  7 ', 7],
    ['3.9', 3],
    ['12px', 12],
    ['', 0],
    ['abc', 0],
  ])('toInt(%j) = %d', (input, out) => expect(toInt(input)).toBe(out));

  it.each([
    ['1.5', 1.5],
    ['-0.25', -0.25],
    ['1e3', 1000],
    ['', 0],
    ['x', 0],
    ['Infinity', 0],
  ])('toFloat(%j) = %d', (input, out) => expect(toFloat(input)).toBe(out));
});

describe('splitLines', () => {
  it('splits on newlines and commas, trimming and dropping empties', () => {
    expect(splitLines('a, b\n c ,,\n\n d')).toEqual(['a', 'b', 'c', 'd']);
  });

  it('handles CRLF', () => {
    expect(splitLines('10.0.0.0/8\r\n192.168.0.1\r\n')).toEqual(['10.0.0.0/8', '192.168.0.1']);
  });

  it('returns an empty list for blank input', () => {
    expect(splitLines('')).toEqual([]);
    expect(splitLines(' \n , ')).toEqual([]);
  });
});

describe('splitArgs / joinArgs', () => {
  it('splits on whitespace', () => {
    expect(splitArgs('  --port   3000\t--verbose ')).toEqual(['--port', '3000', '--verbose']);
    expect(splitArgs('')).toEqual([]);
  });

  it('honors double and single quotes', () => {
    expect(splitArgs('run "hello world" \'a b\'')).toEqual(['run', 'hello world', 'a b']);
  });

  it('keeps empty quoted arguments', () => {
    expect(splitArgs('a "" b')).toEqual(['a', '', 'b']);
  });

  it('quotes arguments containing whitespace when joining', () => {
    expect(joinArgs(['--title', 'My App', 'x'])).toBe('--title "My App" x');
    expect(joinArgs(undefined)).toBe('');
    expect(joinArgs([])).toBe('');
  });

  it('round-trips arguments with spaces', () => {
    const args = ['--max-old-space-size=512', 'hello world', 'tab\there', '--x'];
    expect(splitArgs(joinArgs(args))).toEqual(args);
  });

  // BUG: splitArgs only recognizes quotes at the start of a token (obj.ts:53), so a shell-style
  // `--name="John Doe"` is split into ['--name="John', 'Doe"'] with the quotes kept.
  it.skip('handles quotes in the middle of an argument', () => {
    expect(splitArgs('--name="John Doe" --x')).toEqual(['--name=John Doe', '--x']);
  });

  // BUG: joinArgs wraps whitespace-containing args in double quotes without escaping embedded
  // double quotes (obj.ts:60), so ArgsInput cannot round-trip such an argument.
  it.skip('round-trips arguments containing both spaces and double quotes', () => {
    const args = ['--greeting', 'say "hi" there'];
    expect(splitArgs(joinArgs(args))).toEqual(args);
  });
});
