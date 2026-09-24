// Web application firewall types (model/waf.go). Kept apart from types.ts,
// which only references WAFConfig and WAFSettings.

export type WAFMode = 'off' | 'detect' | 'block';

export type WAFCategory = 'sqli' | 'xss' | 'lfi' | 'rfi' | 'rce' | 'nodejs' | 'php' | 'java' | 'scanner' | 'protocol';

/** Stops rules firing where they are wrong (Azure WAF / ModSecurity exclusions). */
export interface WAFExclusion {
  /** Path prefix; empty = the whole site. */
  path?: string;
  ruleIds?: number[];
  categories?: WAFCategory[] | string[];
  /** Query string / form fields, JSON keys as dotted paths; a trailing * matches a prefix. */
  args?: string[];
  cookies?: string[];
  headers?: string[];
  comment?: string;
}

/** A site's firewall (routing.waf). A missing mode is off. */
export interface WAFConfig {
  mode?: WAFMode | '';
  /** 1-3; 0/absent = 1. */
  paranoiaLevel?: number;
  /** Anomaly score that blocks; 0/absent = 5. */
  anomalyThreshold?: number;
  /** Body inspection limit; 0/absent = 128. */
  inspectBodyKB?: number;
  exclusions?: WAFExclusion[];
}

/** Server-wide firewall settings (Settings.waf). */
export interface WAFSettings {
  defaultMode: WAFMode;
  defaultParanoiaLevel: number;
  defaultAnomalyThreshold: number;
  eventRetentionDays: number;
}

export type WAFSeverity = 'critical' | 'error' | 'warning' | 'notice';

export type WAFMatchIn = 'path' | 'arg' | 'argName' | 'cookie' | 'header' | 'file' | 'body' | 'request' | 'query';

export interface WAFMatch {
  ruleId: number;
  category: WAFCategory | string;
  severity: WAFSeverity | string;
  score: number;
  message: string;
  in: WAFMatchIn | string;
  name?: string;
  snippet?: string;
}

export type WAFAction = 'blocked' | 'detected';

export interface WAFEvent {
  seq: number;
  /** The request ID shown on the block page. */
  id: string;
  time: string;
  siteId: string;
  /** The deployment slot that served the request; absent = production. */
  slot?: string;
  action: WAFAction | string;
  clientIp: string;
  method: string;
  host: string;
  /** Without the query string; token-like segments read "[redacted]". Attacker-controlled: render as text only. */
  path: string;
  userAgent?: string;
  score: number;
  threshold: number;
  paranoiaLevel: number;
  matches: WAFMatch[];
}

export interface WAFRuleInfo {
  id: number;
  category: WAFCategory | string;
  severity: WAFSeverity | string;
  score: number;
  paranoiaLevel: number;
  message: string;
}

export interface WAFCounters {
  inspected: number;
  blocked: number;
  detected: number;
  matches: Record<string, number>;
}

/** GET/PUT /api/sites/{id}/waf */
export interface SiteWAF {
  config: WAFConfig;
  stats: WAFCounters;
}

export interface WAFEventFilter {
  siteId?: string;
  action?: WAFAction | '';
  ip?: string;
  rule?: number | '';
  category?: string;
  requestId?: string;
  since?: string;
  before?: number;
  limit?: number;
}
