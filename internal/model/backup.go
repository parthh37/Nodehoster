package model

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// BackupSettings schedule backups of this server to other machines or
// cloud storage, the way Windows Server Backup or a nightly robocopy job
// would, except that NodeHoster knows what to copy: its configuration, its
// certificates and the sites' shared folders. The binaries, releases and
// Node.js runtimes are left out: a new server gets them from the installer
// and a redeploy.
type BackupSettings struct {
	Enabled bool `json:"enabled"`
	// Time is when the scheduled backup runs, "HH:MM" in the server's
	// local time; Weekdays restricts it to some days (0 = Sunday … 6 =
	// Saturday), empty meaning every day.
	Time     string `json:"time"`
	Weekdays []int  `json:"weekdays"`

	// Retention, applied at each destination after an upload, to this
	// server's own archives only. A backup is kept when either rule keeps
	// it; with neither set, nothing is ever deleted.
	KeepLast int `json:"keepLast"` // the newest N archives
	KeepDays int `json:"keepDays"` // archives younger than N days

	// Contents. The configuration (settings, sites, certificate records)
	// is always included.
	IncludeCertificates bool `json:"includeCertificates"` // PEM files and private keys
	IncludeShared       bool `json:"includeShared"`       // sites/<id>/shared
	// SharedSiteIDs limits IncludeShared to some sites; empty = all.
	SharedSiteIDs []string `json:"sharedSiteIds"`

	// Passphrase encrypts archives and carries the secrets in a form a
	// replacement server can read (secret). Without it an archive can only
	// be restored on this machine: secrets stay sealed with its master key.
	Passphrase string `json:"passphrase,omitempty"`

	Destinations []BackupDestination `json:"destinations"`
}

// Backup destination types.
const (
	BackupFolder = "folder"
	BackupS3     = "s3"
	BackupAzure  = "azure"
	BackupSFTP   = "sftp"
)

// BackupDestination is one place archives are copied to. Only the section
// matching Type is used.
type BackupDestination struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Type    string      `json:"type"` // folder | s3 | azure | sftp
	Enabled bool        `json:"enabled"`
	Folder  *FolderDest `json:"folder,omitempty"`
	S3      *S3Dest     `json:"s3,omitempty"`
	Azure   *AzureDest  `json:"azure,omitempty"`
	SFTP    *SFTPDest   `json:"sftp,omitempty"`
}

// FolderDest is a local folder or a UNC path (\\server\share\backups).
type FolderDest struct {
	Path string `json:"path"`
}

// S3Dest is any S3-compatible bucket: AWS S3, Cloudflare R2, Backblaze B2,
// MinIO, Wasabi...
type S3Dest struct {
	Endpoint        string `json:"endpoint,omitempty"` // "" = AWS for the region
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"` // secret
	PathStyle       bool   `json:"pathStyle"`       // endpoint/bucket/key instead of bucket.endpoint/key (MinIO)
}

// AzureDest is an Azure Blob Storage container, authorized by a SAS token
// or the account key.
type AzureDest struct {
	Account    string `json:"account"`
	Container  string `json:"container"`
	Prefix     string `json:"prefix,omitempty"`
	SASToken   string `json:"sasToken,omitempty"`   // secret
	AccountKey string `json:"accountKey,omitempty"` // secret
	// Endpoint overrides https://<account>.blob.core.windows.net, for
	// sovereign clouds or the Azurite emulator.
	Endpoint string `json:"endpoint,omitempty"`
}

// SFTPDest is a directory on an SSH server. HostKey is required: the
// server's key is checked on every connection, never trusted on first use.
type SFTPDest struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`   // secret
	PrivateKey string `json:"privateKey,omitempty"` // secret, PEM (OpenSSH or PKCS#8)
	Passphrase string `json:"passphrase,omitempty"` // secret, for the private key
	Directory  string `json:"directory"`
	HostKey    string `json:"hostKey"` // SHA256:… fingerprint, as ssh-keygen -lf prints it
}

// BackupRun is one backup, kept in the history.
type BackupRun struct {
	ID         string    `json:"id"`
	Trigger    string    `json:"trigger"` // schedule | manual
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// Status is success (every destination has the archive), partial
	// (some have it) or failed (none has it).
	Status       string             `json:"status"`
	Error        string             `json:"error,omitempty"` // building the archive failed
	File         string             `json:"file,omitempty"`
	Size         int64              `json:"size"`
	Encrypted    bool               `json:"encrypted"`
	Contents     []string           `json:"contents"` // configuration, certificates, shared:<site name>
	Destinations []BackupDestResult `json:"destinations"`
}

type BackupDestResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Pruned int    `json:"pruned"` // old archives deleted by retention
}

// BackupStatus is the state of backups, for the Backups pages.
type BackupStatus struct {
	Enabled      bool        `json:"enabled"`
	Running      bool        `json:"running"`
	RunningSince *time.Time  `json:"runningSince,omitempty"`
	RunningWhat  string      `json:"runningWhat,omitempty"` // schedule | manual | restore | download
	NextRun      *time.Time  `json:"nextRun,omitempty"`
	Encrypted    bool        `json:"encrypted"` // a passphrase is set
	Hostname     string      `json:"hostname"`  // as it appears in archive names
	History      []BackupRun `json:"history"`   // newest first
}

// RestoreResult reports what a restore did.
type RestoreResult struct {
	Format       string     `json:"format"` // json | archive
	Hostname     string     `json:"hostname,omitempty"`
	Created      *time.Time `json:"created,omitempty"`
	Encrypted    bool       `json:"encrypted"`
	Sites        int        `json:"sites"`
	Certificates int        `json:"certificates"` // certificates whose files were restored
	SharedSites  []string   `json:"sharedSites"`  // names
	Warnings     []string   `json:"warnings"`
}

const (
	BackupSuccess = "success"
	BackupPartial = "partial"
	BackupFailed  = "failed"
)

var (
	backupTimeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	hostKeyRE    = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}=?$`)
	s3BucketRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]{1,62}$`)
	azNameRE     = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	azContRE     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9]|-[a-z0-9]){2,62}$`)
)

// DefaultBackup is how a server starts: off, nightly at 02:30, the last
// fourteen archives, configuration and certificates.
func DefaultBackup() BackupSettings {
	return BackupSettings{Time: "02:30", KeepLast: 14, IncludeCertificates: true, Weekdays: []int{}, SharedSiteIDs: []string{}, Destinations: []BackupDestination{}}
}

// Validate checks the schedule and every destination. Secrets are checked
// for presence only (a masked value counts as present).
func (b *BackupSettings) Validate() error {
	if !backupTimeRE.MatchString(b.Time) {
		return verr("backup.time", "use HH:MM, 24-hour, e.g. 02:30")
	}
	seen := map[int]bool{}
	for _, d := range b.Weekdays {
		if d < 0 || d > 6 || seen[d] {
			return verr("backup.weekdays", "weekdays are 0 (Sunday) to 6 (Saturday), each once")
		}
		seen[d] = true
	}
	slices.Sort(b.Weekdays)
	if b.KeepLast < 0 || b.KeepDays < 0 {
		return verr("backup.keepLast", "cannot be negative")
	}
	ids := map[string]bool{}
	for i := range b.Destinations {
		d := &b.Destinations[i]
		f := fmt.Sprintf("backup.destinations[%d]", i)
		d.Name = strings.TrimSpace(d.Name)
		if d.Name == "" {
			return verr(f+".name", "give the destination a name")
		}
		if ids[d.ID] {
			return verr(f+".id", "duplicate destination")
		}
		ids[d.ID] = true
		if err := d.Validate(f); err != nil {
			return err
		}
	}
	if b.Enabled && len(b.Destinations) == 0 {
		return verr("backup.destinations", "add a destination before enabling scheduled backups")
	}
	return nil
}

// Validate checks one destination; f is the field path for errors.
func (d *BackupDestination) Validate(f string) error {
	switch d.Type {
	case BackupFolder:
		d.S3, d.Azure, d.SFTP = nil, nil, nil
		if d.Folder == nil || strings.TrimSpace(d.Folder.Path) == "" {
			return verr(f+".folder.path", "enter a folder or a UNC path such as \\\\server\\share\\backups")
		}
		d.Folder.Path = strings.TrimSpace(d.Folder.Path)
		if !isAbsPath(d.Folder.Path) {
			return verr(f+".folder.path", "use an absolute path, e.g. D:\\Backups or \\\\server\\share\\backups")
		}
	case BackupS3:
		d.Folder, d.Azure, d.SFTP = nil, nil, nil
		s := d.S3
		if s == nil {
			return verr(f+".s3.bucket", "enter the bucket")
		}
		s.Endpoint = strings.TrimRight(strings.TrimSpace(s.Endpoint), "/")
		s.Region = strings.TrimSpace(s.Region)
		s.Prefix = cleanPrefix(s.Prefix)
		if s.Endpoint != "" {
			if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return verr(f+".s3.endpoint", "use an https:// URL, e.g. https://<account>.r2.cloudflarestorage.com")
			}
		}
		if s.Region == "" {
			if s.Endpoint == "" {
				return verr(f+".s3.region", "enter the bucket's region, e.g. eu-west-1")
			}
			s.Region = "us-east-1" // what most S3-compatible services accept
		}
		if !s3BucketRE.MatchString(s.Bucket) {
			return verr(f+".s3.bucket", "not a valid bucket name")
		}
		if strings.TrimSpace(s.AccessKeyID) == "" {
			return verr(f+".s3.accessKeyId", "enter the access key ID")
		}
		if s.SecretAccessKey == "" {
			return verr(f+".s3.secretAccessKey", "enter the secret access key")
		}
	case BackupAzure:
		d.Folder, d.S3, d.SFTP = nil, nil, nil
		a := d.Azure
		if a == nil {
			return verr(f+".azure.account", "enter the storage account name")
		}
		a.Prefix = cleanPrefix(a.Prefix)
		a.Endpoint = strings.TrimRight(strings.TrimSpace(a.Endpoint), "/")
		a.SASToken = strings.TrimPrefix(strings.TrimSpace(a.SASToken), "?")
		if !azNameRE.MatchString(a.Account) {
			return verr(f+".azure.account", "a storage account name is 3-24 lowercase letters and digits")
		}
		if !azContRE.MatchString(a.Container) {
			return verr(f+".azure.container", "a container name is 3-63 lowercase letters, digits and single hyphens")
		}
		if a.Endpoint != "" {
			if u, err := url.Parse(a.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return verr(f+".azure.endpoint", "use an https:// URL")
			}
		}
		if a.SASToken == "" && a.AccountKey == "" {
			return verr(f+".azure.sasToken", "enter a SAS token or the account key")
		}
	case BackupSFTP:
		d.Folder, d.S3, d.Azure = nil, nil, nil
		s := d.SFTP
		if s == nil || strings.TrimSpace(s.Host) == "" {
			return verr(f+".sftp.host", "enter the server's host name")
		}
		s.Host = strings.TrimSpace(s.Host)
		if s.Port == 0 {
			s.Port = 22
		}
		if s.Port < 1 || s.Port > 65535 {
			return verr(f+".sftp.port", "not a valid port")
		}
		if strings.TrimSpace(s.Username) == "" {
			return verr(f+".sftp.username", "enter the user name")
		}
		if s.Password == "" && s.PrivateKey == "" {
			return verr(f+".sftp.password", "enter a password or a private key")
		}
		s.HostKey = strings.TrimSpace(s.HostKey)
		if !hostKeyRE.MatchString(s.HostKey) {
			return verr(f+".sftp.hostKey", "enter the server's SHA256 host key fingerprint; use Test to read it from the server")
		}
		if s.Directory == "" {
			s.Directory = "."
		}
	default:
		return verr(f+".type", "choose folder, s3, azure or sftp")
	}
	return nil
}

// cleanPrefix makes an object name prefix end in "/" (or be empty).
func cleanPrefix(p string) string {
	p = strings.Trim(strings.TrimSpace(strings.ReplaceAll(p, "\\", "/")), "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// isAbsPath accepts Windows drive and UNC paths whatever the OS the
// server is built for, and Unix paths.
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}
