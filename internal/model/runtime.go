package model

import "time"

// SiteState is the live, non-persisted state of a site.
type SiteState string

const (
	StateStopped  SiteState = "stopped"
	StateStarting SiteState = "starting"
	StateRunning  SiteState = "running"
	StateDegraded SiteState = "degraded" // some instances down / unhealthy
	StateStopping SiteState = "stopping"
	StateFailed   SiteState = "failed" // rapid-fail protection tripped
)

type InstanceStatus struct {
	Index        int        `json:"index"`
	PID          int        `json:"pid"`
	Port         int        `json:"port"`
	State        string     `json:"state"` // starting | ready | unhealthy | stopping | exited | crashed
	Healthy      bool       `json:"healthy"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	Restarts     int        `json:"restarts"`
	LastExitCode *int       `json:"lastExitCode,omitempty"`
	LastExitAt   *time.Time `json:"lastExitAt,omitempty"`
	CPUPercent   float64    `json:"cpuPercent"`
	MemoryBytes  uint64     `json:"memoryBytes"`
	Requests     int64      `json:"requests"`
	ActiveConns  int64      `json:"activeConns"`

	// Reported by the in-process agent when enabled.
	HeapUsedBytes  uint64  `json:"heapUsedBytes,omitempty"`
	HeapTotalBytes uint64  `json:"heapTotalBytes,omitempty"`
	EventLoopLagMs float64 `json:"eventLoopLagMs,omitempty"`
	NodeVersion    string  `json:"nodeVersion,omitempty"`
}

type TrafficStats struct {
	Requests     int64   `json:"requests"`
	Status2xx    int64   `json:"status2xx"`
	Status3xx    int64   `json:"status3xx"`
	Status4xx    int64   `json:"status4xx"`
	Status5xx    int64   `json:"status5xx"`
	BytesIn      int64   `json:"bytesIn"`
	BytesOut     int64   `json:"bytesOut"`
	AvgLatencyMs float64 `json:"avgLatencyMs"`
	RPS          float64 `json:"rps"` // requests per second over the last minute
}

type SiteStatus struct {
	SiteID    string           `json:"siteId"`
	State     SiteState        `json:"state"`
	Message   string           `json:"message,omitempty"`
	Instances []InstanceStatus `json:"instances"`
	Traffic   TrafficStats     `json:"traffic"`
	Upstreams []UpstreamStatus `json:"upstreams,omitempty"`
	Cache     *CacheStats      `json:"cache,omitempty"` // when the response cache is enabled
}

type UpstreamStatus struct {
	URL         string `json:"url"`
	Local       bool   `json:"local,omitempty"` // this server's own instances in a load-balanced node site
	Healthy     bool   `json:"healthy"`
	ActiveConns int64  `json:"activeConns"`
	LastError   string `json:"lastError,omitempty"`
}

type MetricPoint struct {
	Time        time.Time `json:"t"`
	Requests    int64     `json:"req"`
	Errors      int64     `json:"err"`
	AvgLatency  float64   `json:"lat"`
	CPUPercent  float64   `json:"cpu"`
	MemoryBytes uint64    `json:"mem"`
}

type ServerInfo struct {
	Version    string    `json:"version"`
	Commit     string    `json:"commit"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	StartedAt  time.Time `json:"startedAt"`
	CPUPercent float64   `json:"cpuPercent"`
	CPUCount   int       `json:"cpuCount"`
	MemTotal   uint64    `json:"memTotal"`
	MemUsed    uint64    `json:"memUsed"`
	DiskTotal  uint64    `json:"diskTotal"`
	DiskFree   uint64    `json:"diskFree"`
	DataDir    string    `json:"dataDir"`
	Listeners  []string  `json:"listeners"`
	GoVersion  string    `json:"goVersion"`
	IsService  bool      `json:"isService"`

	// The web console: its URL, or why it is not listening (it does not
	// stop the server; the desktop manager can still fix it).
	AdminURL   string `json:"adminUrl,omitempty"`
	AdminError string `json:"adminError,omitempty"`
}

type LogLine struct {
	Time     time.Time `json:"t"`
	Stream   string    `json:"s"` // stdout | stderr | system
	Instance int       `json:"i"`
	Text     string    `json:"m"`
	Slot     string    `json:"slot,omitempty"` // the deployment slot that wrote it; "" = production
}
