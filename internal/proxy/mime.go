package proxy

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// builtinMime is the MIME table static files are served with. Go's mime
// package reads the Windows registry, where other software often maps
// .js to text/plain or .css to something odd, and browsers then refuse
// module scripts and style sheets; so NodeHoster ships its own table, as
// IIS does, and the registry is never consulted. Text types carry a
// charset because browsers otherwise guess one.
var builtinMime = map[string]string{
	// Web documents and code
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".xhtml":       "application/xhtml+xml",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".cjs":         "text/javascript; charset=utf-8",
	".map":         "application/json",
	".json":        "application/json",
	".jsonld":      "application/ld+json",
	".webmanifest": "application/manifest+json",
	".xml":         "application/xml",
	".xsl":         "application/xml",
	".xslt":        "application/xslt+xml",
	".rss":         "application/rss+xml",
	".atom":        "application/atom+xml",
	".wasm":        "application/wasm",
	".txt":         "text/plain; charset=utf-8",
	".text":        "text/plain; charset=utf-8",
	".md":          "text/markdown; charset=utf-8",
	".csv":         "text/csv; charset=utf-8",
	".tsv":         "text/tab-separated-values; charset=utf-8",
	".ics":         "text/calendar; charset=utf-8",
	".vcf":         "text/vcard; charset=utf-8",
	".vtt":         "text/vtt; charset=utf-8",
	".srt":         "application/x-subrip",
	".yaml":        "application/yaml",
	".yml":         "application/yaml",
	".toml":        "application/toml",
	".appcache":    "text/cache-manifest",

	// Images
	".png":  "image/png",
	".apng": "image/apng",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".jpe":  "image/jpeg",
	".jfif": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".heic": "image/heic",
	".heif": "image/heif",
	".jxl":  "image/jxl",
	".svg":  "image/svg+xml",
	".svgz": "image/svg+xml",
	".ico":  "image/x-icon",
	".cur":  "image/x-icon",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",

	// Fonts
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".eot":   "application/vnd.ms-fontobject",

	// Audio and video
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".oga":  "audio/ogg",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
	".weba": "audio/webm",
	".flac": "audio/flac",
	".mid":  "audio/midi",
	".midi": "audio/midi",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".mov":  "video/quicktime",
	".avi":  "video/x-msvideo",
	".mkv":  "video/x-matroska",
	".mpeg": "video/mpeg",
	".mpg":  "video/mpeg",
	".m3u8": "application/vnd.apple.mpegurl",
	".ts":   "video/mp2t",
	".mpd":  "application/dash+xml",

	// Documents
	".pdf":  "application/pdf",
	".rtf":  "application/rtf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".epub": "application/epub+zip",

	// Archives and downloads
	".zip": "application/zip",
	".gz":  "application/gzip",
	".tgz": "application/gzip",
	".tar": "application/x-tar",
	".7z":  "application/x-7z-compressed",
	".rar": "application/vnd.rar",
	".bz2": "application/x-bzip2",
	".xz":  "application/x-xz",
	".zst": "application/zstd",
	".br":  "application/octet-stream",
	".exe": "application/vnd.microsoft.portable-executable",
	".msi": "application/x-msi",
	".dmg": "application/x-apple-diskimage",
	".apk": "application/vnd.android.package-archive",
	".deb": "application/vnd.debian.binary-package",
	".rpm": "application/x-rpm",
	".iso": "application/x-iso9660-image",
	".bin": "application/octet-stream",
	".jar": "application/java-archive",
	".crx": "application/x-chrome-extension",
	".xpi": "application/x-xpinstall",

	// Certificates and keys that are meant to be public
	".cer": "application/pkix-cert",
	".crt": "application/x-x509-ca-cert",
	".crl": "application/pkix-crl",
	".p7b": "application/x-pkcs7-certificates",

	// 3D and data
	".gltf":     "model/gltf+json",
	".glb":      "model/gltf-binary",
	".obj":      "model/obj",
	".stl":      "model/stl",
	".usdz":     "model/vnd.usdz+zip",
	".geojson":  "application/geo+json",
	".topojson": "application/json",
	".pbf":      "application/x-protobuf",
	".parquet":  "application/vnd.apache.parquet",
}

// DefaultMimeTypes is the built-in table sorted by extension, for the API.
func DefaultMimeTypes() []model.MimeMap {
	out := make([]model.MimeMap, 0, len(builtinMime))
	for ext, t := range builtinMime {
		out = append(out, model.MimeMap{Extension: ext, Type: t})
	}
	slices.SortFunc(out, func(a, b model.MimeMap) int { return strings.Compare(a.Extension, b.Extension) })
	return out
}

// mimeTypes resolves a file's Content-Type for one site: the site's own
// mappings, then the server's, then the built-in table. It is built with
// the site runtime and read-only afterwards.
type mimeTypes struct {
	custom  map[string]string // site and server mappings merged, site winning
	unknown string            // serve | deny
}

func newMimeTypes(server model.MimeSettings, site model.RoutingConfig) *mimeTypes {
	m := &mimeTypes{custom: map[string]string{}, unknown: server.UnknownTypes}
	for _, mm := range server.Types {
		m.custom[strings.ToLower(mm.Extension)] = strings.TrimSpace(mm.Type)
	}
	for _, mm := range site.MimeTypes {
		m.custom[strings.ToLower(mm.Extension)] = strings.TrimSpace(mm.Type)
	}
	if site.UnknownMimeTypes != "" {
		m.unknown = site.UnknownMimeTypes
	}
	if m.unknown == "" {
		m.unknown = model.UnknownMimeServe
	}
	return m
}

// lookup returns the Content-Type for a file name, and false when the
// extension has no mapping and unknown extensions are refused. A nil
// table is the built-in one.
func (m *mimeTypes) lookup(name string) (string, bool) {
	if m == nil {
		m = &mimeTypes{unknown: model.UnknownMimeServe}
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = "." // how IIS names files without an extension
	}
	if t, ok := m.custom[ext]; ok {
		return t, true
	}
	if t, ok := builtinMime[ext]; ok {
		return t, true
	}
	if m.unknown == model.UnknownMimeDeny {
		return "", false
	}
	return "application/octet-stream", true
}
