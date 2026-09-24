// Resource alerts: mirrors of internal/model/alerts.go and the endpoints of
// docs/API.md "Resource alerts". The rules themselves are saved with the
// settings (Settings.alerts) and the site (Site.alerts).

import { http, qs } from './client';

export type AlertMetric =
  | 'cpu'
  | 'instanceCpu'
  | 'memory'
  | 'memoryPercent'
  | 'eventLoopLag'
  | 'errorRate'
  | 'latency'
  | 'latencyP95'
  | 'instancesDown'
  | 'serverCpu'
  | 'serverMemory'
  | 'diskFree';

export type AlertSeverity = 'warning' | 'critical';

export interface AlertRule {
  id: string;
  metric: AlertMetric | string;
  threshold: number;
  /** 0 = fire on the first evaluation past the limit. */
  forMinutes: number;
  severity: AlertSeverity | string;
  /** errorRate, latency, latencyP95 only. */
  windowMinutes?: number;
  minRequests?: number;
  /** Reminders while firing; 0 = notify once. */
  repeatHours?: number;
  /** On a site: turns off the server-wide rule of the same ID. */
  disabled?: boolean;
}

export interface AlertSettings {
  enabled: boolean;
  siteRules: AlertRule[] | null;
  serverRules: AlertRule[] | null;
  recoveryMinutes: number;
  emailTo: string[] | null;
}

export interface SiteAlerts {
  disabled?: boolean;
  rules?: AlertRule[];
}

export interface AlertSilence {
  /** Absent = acknowledged: silent until the alert resolves. */
  until?: string;
  by: string;
  at: string;
  note?: string;
}

export type AlertState = 'pending' | 'firing' | 'resolved';

export interface Alert {
  id: string;
  ruleId: string;
  /** Absent for the server's own alerts. */
  siteId?: string;
  /** A deployment slot of the site (siteName is then "shop (staging)"); absent for production. */
  slot?: string;
  siteName?: string;
  metric: AlertMetric | string;
  severity: AlertSeverity | string;
  threshold: number;
  forMinutes: number;
  state: AlertState | string;
  value: number;
  peak: number;
  detail?: string;
  message: string;
  since: string;
  firedAt?: string;
  resolvedAt?: string;
  resolveNote?: string;
  notified: boolean;
  lastNotifiedAt?: string;
  silence?: AlertSilence;
}

export interface AlertList {
  enabled: boolean;
  firing: Alert[];
  pending: Alert[];
}

export interface AlertSilenceRequest {
  /** 0 = until the alert resolves (acknowledge). */
  minutes: number;
  note?: string;
}

/** GET /api/sites/{id}/alert-rules: what every site gets, readable by the site's viewers. */
export interface SiteAlertRules {
  enabled: boolean;
  defaults: AlertRule[];
  recoveryMinutes: number;
}

const enc = encodeURIComponent;

export const alertsApi = {
  siteRules: (siteId: string) => http.get<SiteAlertRules>(`/api/sites/${enc(siteId)}/alert-rules`),
  /** Firing and pending alerts, of the sites the caller can see. */
  list: (siteId?: string) => http.get<AlertList>(`/api/alerts${qs({ siteId })}`),
  history: (p: { siteId?: string; server?: boolean; limit?: number } = {}) =>
    http.get<Alert[]>(`/api/alerts/history${qs({ siteId: p.siteId, server: p.server ? 1 : undefined, limit: p.limit })}`),
  silence: (id: string, body: AlertSilenceRequest) => http.post<Alert>(`/api/alerts/${enc(id)}/silence`, body),
  unsilence: (id: string) => http.del<Alert>(`/api/alerts/${enc(id)}/silence`),
};
