import { describe, expect, it } from 'vitest';
import {
  branchPatternError,
  canHavePreviews,
  defaultPreviewConfig,
  describePreview,
  exampleHost,
  failureText,
  groupSites,
  hostPatternError,
  matchBranch,
  previewConfigOf,
  slugBranch,
} from './previews';

describe('hostPatternError', () => {
  it.each(['pr-{number}.preview.example.com', '{branch}.preview.example.com', 'pr-{number}-{branch}.x.io', ' PR-{number}.Example.com '])('accepts %j', (p) => {
    expect(hostPatternError(p)).toBeNull();
  });
  it.each([
    ['', 'Enter'],
    ['{branch}', 'domain'],
    ['preview.example.com', '{number} or {branch}'],
    ['pr-{number}.{branch}.example.com', 'first label only'],
    ['pr_{number}.example.com', 'letters, digits'],
    ['{number}.bad_host.com', 'not a valid host'],
    ['a'.repeat(41) + '{number}.example.com', 'too little room'],
  ])('rejects %j', (p, msg) => {
    expect(hostPatternError(p)).toContain(msg);
  });
});

describe('exampleHost', () => {
  it('fills the placeholders like the server', () => {
    expect(exampleHost('pr-{number}.preview.example.com', 'pr', 42, 'fix/login')).toBe('pr-42.preview.example.com');
    expect(exampleHost('{branch}.preview.example.com', 'pr', 42, 'Fix/Login_Page')).toBe('fix-login-page.preview.example.com');
    expect(exampleHost('pr-{number}-{branch}.x.io', 'branch', 0, 'feature/x')).toBe('pr-feature-x.x.io');
    // A branch preview under a pattern without {branch}: the branch is the label.
    expect(exampleHost('pr-{number}.x.io', 'branch', 0, 'feature/x')).toBe('feature-x.x.io');
  });
  it('slugs branches', () => {
    expect(slugBranch('--Release/1.2.3--')).toBe('release-1-2-3');
  });
});

describe('matchBranch', () => {
  it.each([
    [['feature/*'], 'feature/login', true],
    [['feature/*'], 'feature/a/b', false],
    [['feature/**'], 'feature/a/b', true],
    [['release-?'], 'release-1', true],
    [['release-?'], 'release-10', false],
    [['*'], 'a/b', false],
    [[], 'main', false],
    [['dev', 'hotfix-*-x'], 'hotfix-12-x', true],
  ] as [string[], string, boolean][])('%j matches %j: %s', (patterns, branch, want) => {
    expect(matchBranch(patterns, branch)).toBe(want);
  });
  it('validates patterns', () => {
    expect(branchPatternError('feature/*')).toBeNull();
    expect(branchPatternError('--evil')).not.toBeNull();
    expect(branchPatternError('a..b')).not.toBeNull();
    expect(branchPatternError('has space')).not.toBeNull();
  });
});

describe('groupSites', () => {
  it('puts previews under their parent', () => {
    const sites = [
      { id: 'shop' },
      { id: 'pr-1', previewOf: 'shop' },
      { id: 'pr-2', previewOf: 'shop' },
      { id: 'orphan', previewOf: 'gone' },
      { id: 'blog' },
    ];
    const { roots, previews } = groupSites(sites);
    expect(roots.map((s) => s.id)).toEqual(['shop', 'orphan', 'blog']);
    expect(previews.get('shop')?.map((s) => s.id)).toEqual(['pr-1', 'pr-2']);
    expect(previews.has('blog')).toBe(false);
  });
});

describe('preview settings', () => {
  it('fills in a site saved before previews existed', () => {
    const cfg = previewConfigOf({ deploy: { git: {}, keepReleases: 5 } });
    expect(cfg).toEqual(defaultPreviewConfig());
    expect(cfg.enabled).toBe(false);
    const partial = previewConfigOf({
      deploy: { git: {}, keepReleases: 5, previews: { ...defaultPreviewConfig(), enabled: true, basicAuth: { enabled: true, realm: 'R', users: null } } },
    });
    expect(partial.enabled).toBe(true);
    expect(partial.basicAuth.users).toEqual([]);
  });
  it('knows which sites can have previews', () => {
    expect(canHavePreviews({ type: 'node' })).toBe(true);
    expect(canHavePreviews({ type: 'static' })).toBe(true);
    expect(canHavePreviews({ type: 'proxy' })).toBe(false);
    expect(canHavePreviews({ type: 'node', previewOf: 'x' })).toBe(false);
  });
  it('shows why a deployment failed', () => {
    expect(failureText('— fetch failed: exit status 128')).toBe('fetch failed: exit status 128');
    expect(failureText('Add login — npm ci failed')).toBe('Add login — npm ci failed');
    expect(failureText(undefined)).toBe('');
  });
  it('describes a preview', () => {
    expect(describePreview({ kind: 'pr', number: 42, branch: 'fix' })).toBe('#42 fix');
    expect(describePreview({ kind: 'branch', branch: 'feature/x' })).toBe('branch feature/x');
  });
});
