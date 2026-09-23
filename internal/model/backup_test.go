package model

import (
	"errors"
	"strings"
	"testing"
)

func TestBackupSettingsValidate(t *testing.T) {
	t.Parallel()
	hk := "SHA256:" + strings.Repeat("x", 43)
	ok := func() BackupSettings {
		b := DefaultBackup()
		b.Destinations = []BackupDestination{{ID: "1", Name: "NAS", Type: BackupFolder, Folder: &FolderDest{Path: `\\nas\backups\web`}}}
		return b
	}
	for _, tc := range []struct {
		name  string
		edit  func(b *BackupSettings)
		field string // "" = valid
	}{
		{"default with a folder", func(b *BackupSettings) {}, ""},
		{"drive path", func(b *BackupSettings) { b.Destinations[0].Folder.Path = `D:\Backups` }, ""},
		{"relative path", func(b *BackupSettings) { b.Destinations[0].Folder.Path = `backups` }, "backup.destinations[0].folder.path"},
		{"bad time", func(b *BackupSettings) { b.Time = "24:00" }, "backup.time"},
		{"weekday out of range", func(b *BackupSettings) { b.Weekdays = []int{7} }, "backup.weekdays"},
		{"weekday twice", func(b *BackupSettings) { b.Weekdays = []int{1, 1} }, "backup.weekdays"},
		{"negative retention", func(b *BackupSettings) { b.KeepDays = -1 }, "backup.keepLast"},
		{"enabled without destinations", func(b *BackupSettings) { b.Enabled, b.Destinations = true, nil }, "backup.destinations"},
		{"unnamed", func(b *BackupSettings) { b.Destinations[0].Name = " " }, "backup.destinations[0].name"},
		{"unknown type", func(b *BackupSettings) { b.Destinations[0].Type = "ftp" }, "backup.destinations[0].type"},
		{"s3 on AWS needs a region", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "S3", Type: BackupS3, S3: &S3Dest{Bucket: "nh-backups", AccessKeyID: "a", SecretAccessKey: "b"}}
		}, "backup.destinations[0].s3.region"},
		{"s3 compatible defaults the region", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "B2", Type: BackupS3, S3: &S3Dest{Endpoint: "https://s3.eu-central-003.backblazeb2.com", Bucket: "nh", AccessKeyID: "a", SecretAccessKey: "b"}}
		}, ""},
		{"s3 bad bucket", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "S3", Type: BackupS3, S3: &S3Dest{Region: "x", Bucket: "Bad_Bucket!", AccessKeyID: "a", SecretAccessKey: "b"}}
		}, "backup.destinations[0].s3.bucket"},
		{"s3 endpoint not a URL", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "S3", Type: BackupS3, S3: &S3Dest{Endpoint: "minio:9000", Bucket: "nh", AccessKeyID: "a", SecretAccessKey: "b"}}
		}, "backup.destinations[0].s3.endpoint"},
		{"azure needs a credential", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "Az", Type: BackupAzure, Azure: &AzureDest{Account: "nhstore", Container: "backups"}}
		}, "backup.destinations[0].azure.sasToken"},
		{"azure bad container", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "Az", Type: BackupAzure, Azure: &AzureDest{Account: "nhstore", Container: "Back--ups", SASToken: "sig=x"}}
		}, "backup.destinations[0].azure.container"},
		{"sftp needs the host key", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "SSH", Type: BackupSFTP, SFTP: &SFTPDest{Host: "h", Username: "u", Password: "p"}}
		}, "backup.destinations[0].sftp.hostKey"},
		{"sftp needs a credential", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "SSH", Type: BackupSFTP, SFTP: &SFTPDest{Host: "h", Username: "u", HostKey: hk}}
		}, "backup.destinations[0].sftp.password"},
		{"sftp complete", func(b *BackupSettings) {
			b.Destinations[0] = BackupDestination{ID: "1", Name: "SSH", Type: BackupSFTP, SFTP: &SFTPDest{Host: "h", Username: "u", PrivateKey: "k", HostKey: hk}}
		}, ""},
		{"duplicate ids", func(b *BackupSettings) { b.Destinations = append(b.Destinations, b.Destinations[0]) }, "backup.destinations[1].id"},
	} {
		b := ok()
		tc.edit(&b)
		err := b.Validate()
		var ve *ValidationError
		switch {
		case tc.field == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.field != "" && (!errors.As(err, &ve) || ve.Field != tc.field):
			t.Errorf("%s: err = %v, want field %s", tc.name, err, tc.field)
		}
	}
	// Normalization: prefixes end in a slash, the SFTP port defaults, the
	// other sections are dropped.
	b := ok()
	b.Destinations[0] = BackupDestination{ID: "1", Name: "S3", Type: BackupS3, Folder: &FolderDest{Path: "/x"},
		S3: &S3Dest{Region: "eu-west-1", Bucket: "nh", Prefix: "/servers\\web01", AccessKeyID: "a", SecretAccessKey: "b"}}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if d := b.Destinations[0]; d.S3.Prefix != "servers/web01/" || d.Folder != nil {
		t.Errorf("normalized = %+v %+v", d, d.S3)
	}
}
