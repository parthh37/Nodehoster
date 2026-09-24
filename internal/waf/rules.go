package waf

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// The rules. They are NodeHoster's own, written for RE2 (Go's regexp, which
// runs in time linear in the input: no pattern can be made to backtrack
// for ever) and numbered in the OWASP Core Rule Set's ranges so that the
// categories read familiarly: 913 scanners, 920 protocol, 930 local file
// inclusion, 931 remote file inclusion, 932 command injection, 933 PHP,
// 934 Node.js, 941 XSS, 942 SQL injection, 944 Java. Like the CRS, each
// rule adds its severity's score to the request's anomaly score and
// belongs to a paranoia level: level 1 is meant to have no false positives
// on ordinary traffic, levels 2 and 3 catch more and need exclusions.
//
// Rules match normalised, lowercased text (see normalize.go).

// Severities and their anomaly scores.
const (
	critical = 5
	errSev   = 4
	warning  = 3
	notice   = 2
)

func severityName(score int) string {
	switch score {
	case critical:
		return "critical"
	case errSev:
		return "error"
	case warning:
		return "warning"
	}
	return "notice"
}

// target is where in the request a value came from.
type target uint16

const (
	tPath    target = 1 << iota // the URL path
	tArgName                    // names of query string and form fields, JSON keys
	tArg                        // their values
	tCookie                     // cookie values
	tUA                         // User-Agent
	tReferer                    // Referer
	tHeader                     // every other header but Cookie and Authorization
	tFile                       // uploaded files' names
	tBody                       // a text body inspected whole
	nTargets = iota
)

const (
	tHeaders = tUA | tReferer
	tInput   = tArg | tArgName | tCookie | tBody // what a client fills in
	tAll     = tPath | tInput | tHeaders | tHeader | tFile
)

// form is which rendering of a value a rule matches.
type form uint8

const (
	fNorm    form = iota // decoded, folded, lowercased
	fSQL                 // fNorm with SQL comments replaced by a space
	fRaw                 // as received, lowercased: for encodings themselves
	fDecoded             // decoded but not folded (NUL bytes kept)
)

type rule struct {
	model.WAFRuleInfo
	idx    int // position in rules
	cat    int // position in model.WAFCategories
	in     target
	in2    target // added from paranoia level 2
	form   form
	groups []litSet // each needs one of its literals in the value
	re     *regexp.Regexp
	fn     func(s string) (string, bool) // instead of re: the snippet, and whether it matched
	// request marks a rule checked on the request as a whole, in code
	// (engine.go), not on values.
	request bool
}

// def is a rule as written below.
type def struct {
	id    int
	cat   string
	sev   int
	pl    int
	msg   string
	in    target
	in2   target
	form  form
	lits  [][]string
	re    string
	fn    func(string) (string, bool)
	reqst bool
}

func oneOf(l ...string) []string { return l }

// Shell commands for the command-injection rules.
const shellCmds = `(?:cat|tac|nl|head|tail|more|less|ls|dir|id|whoami|uname|hostname|ifconfig|ipconfig|netstat|ps|pwd|env|printenv|echo|ping|nslookup|dig|traceroute|wget|curl|nc|ncat|netcat|socat|telnet|ssh|scp|tftp|ftp|bash|sh|dash|zsh|ksh|csh|busybox|python[23]?|perl|ruby|php|lua|awk|gawk|sed|find|xargs|chmod|chown|rm|mv|cp|touch|mkdir|kill|killall|pkill|crontab|base64|xxd|openssl|sleep|nohup|sudo|su|useradd|passwd|systeminfo|tasklist|taskkill|net|netsh|reg|wmic|powershell|pwsh|cmd|certutil|bitsadmin|mshta|rundll32|regsvr32|cscript|wscript|type|del)`

// What may follow an injected command: the end, another operator, or an
// argument that looks like one (an option, a path, a number, a quote).
const cmdTail = `(?:\.exe)?(?:\s*$|\s*[;|&><\x60)]|\s+[-/.~$\\\d'"])`

const eventHandlers = `(?:abort|activate|afterprint|animation(?:end|iteration|start|cancel)|auxclick|beforeinput|beforeprint|beforetoggle|beforeunload|begin|blur|canplay|canplaythrough|change|click|close|contextmenu|copy|cuechange|cut|dblclick|drag|dragend|dragenter|dragleave|dragover|dragstart|drop|durationchange|end|ended|error|focus|focusin|focusout|formdata|fullscreenchange|hashchange|input|invalid|keydown|keypress|keyup|load|loadeddata|loadedmetadata|loadend|loadstart|message|mousedown|mouseenter|mouseleave|mousemove|mouseout|mouseover|mouseup|mousewheel|pagehide|pageshow|paste|pause|play|playing|pointer[a-z]+|popstate|progress|readystatechange|repeat|reset|resize|scroll|scrollend|search|seeked|seeking|select|selectionchange|selectstart|show|start|storage|submit|toggle|touch[a-z]+|transition[a-z]+|unload|volumechange|wheel)`

var defs = []def{
	// ---- 913: vulnerability scanners
	{id: 913100, cat: model.WAFScanner, sev: critical, pl: 1, msg: "Vulnerability scanner User-Agent", in: tUA,
		lits: [][]string{oneOf("nikto", "sqlmap", "nmap", "nessus", "openvas", "acunetix", "netsparker", "qualys", "w3af", "dirbuster", "gobuster", "dirb", "wfuzz", "ffuf", "feroxbuster", "nuclei", "zgrab", "masscan", "zmeu", "havij", "arachni", "skipfish", "wpscan", "joomscan", "whatweb", "commix", "xsser", "morfeus", "webinspect", "appscan", "fimap", "bsqlbf", "pangolin", "jaeles", "struts-pwn", "jorgee", "brutus", "hydra", "sqlninja", "absinthe", "nsauditor", "paros", "webshag", "grabber", "metasploit", "x-scan")},
		re:   `\b(?:nikto|sqlmap|nmap|nessus|openvas|acunetix|netsparker|qualys|w3af|dirbuster|gobuster|dirb|wfuzz|ffuf|feroxbuster|nuclei|zgrab|masscan|zmeu|havij|arachni|skipfish|wpscan|joomscan|whatweb|commix|xsser|morfeus|webinspect|appscan|fimap|bsqlbf|pangolin|jaeles|struts-pwn|jorgee|brutus|hydra|sqlninja|absinthe|nsauditor|paros|webshag|grabber|metasploit|x-scan)\b`},
	{id: 913110, cat: model.WAFScanner, sev: critical, pl: 1, msg: "Vulnerability scanner request header", reqst: true},
	{id: 913120, cat: model.WAFScanner, sev: notice, pl: 3, msg: "Scripting tool or HTTP library User-Agent", in: tUA,
		lits: [][]string{oneOf("curl", "wget", "python", "go-http-client", "libwww-perl", "java/", "httpclient", "okhttp", "aiohttp", "powershell", "winhttp", "axios", "node-fetch", "undici", "httpie", "guzzle", "ruby", "scrapy", "urllib")},
		re:   `^(?:curl|wget|python-requests|python-urllib|python-httpx|go-http-client|libwww-perl|java/|apache-httpclient|okhttp|aiohttp|mozilla/\d\.\d \(windows nt [\d.]+; [^)]*\) windowspowershell|winhttp|axios|node-fetch|undici|httpie|guzzlehttp|ruby|scrapy)`},

	// ---- 920: protocol violations and evasion encodings
	{id: 920100, cat: model.WAFProtocol, sev: critical, pl: 1, msg: "NUL byte in the request", in: tPath | tInput | tHeaders | tFile, form: fDecoded,
		fn: func(s string) (string, bool) {
			i := strings.IndexByte(s, 0)
			if i < 0 {
				return "", false
			}
			return s[max(0, i-20):min(len(s), i+20)], true
		}},
	{id: 920110, cat: model.WAFProtocol, sev: errSev, pl: 1, msg: "Control characters in the path or an argument name", in: tPath | tArgName | tFile, form: fDecoded, fn: controlChars(false)},
	{id: 920115, cat: model.WAFProtocol, sev: warning, pl: 2, msg: "Control characters in an argument", in: tArg | tCookie, form: fDecoded, fn: controlChars(true)},
	{id: 920120, cat: model.WAFProtocol, sev: warning, pl: 1, msg: "Invalid URL encoding in the query string", reqst: true},
	{id: 920130, cat: model.WAFProtocol, sev: warning, pl: 1, msg: "IIS %u Unicode encoding", in: tPath | tArg | tArgName | tCookie, form: fRaw,
		lits: [][]string{oneOf("%u")}, re: `%u[0-9a-f]{4}`},
	{id: 920140, cat: model.WAFProtocol, sev: warning, pl: 2, msg: "Double URL encoding", in: tPath | tArg | tArgName | tCookie, form: fRaw,
		lits: [][]string{oneOf("%25")}, re: `%25[0-9a-f]{2}`},
	{id: 920160, cat: model.WAFProtocol, sev: warning, pl: 2, msg: "GET or HEAD request with a body", reqst: true},
	{id: 920170, cat: model.WAFProtocol, sev: critical, pl: 1, msg: "HTTP method not allowed (TRACE, TRACK, DEBUG)", reqst: true},
	{id: 920180, cat: model.WAFProtocol, sev: notice, pl: 2, msg: "Missing User-Agent header", reqst: true},
	{id: 920190, cat: model.WAFProtocol, sev: warning, pl: 1, msg: "Malformed Content-Type header", reqst: true},
	{id: 920200, cat: model.WAFProtocol, sev: notice, pl: 1, msg: "Request body could not be parsed", reqst: true},
	{id: 920205, cat: model.WAFProtocol, sev: critical, pl: 1, msg: "JSON body nested too deeply", reqst: true},
	// 920210, 920220 and 920240 make a request fail closed (Result.Incomplete).
	{id: 920210, cat: model.WAFProtocol, sev: warning, pl: 1, msg: "Too many arguments to inspect", reqst: true},
	{id: 920220, cat: model.WAFProtocol, sev: critical, pl: 1, msg: "Request too costly to inspect completely", reqst: true},
	{id: 920240, cat: model.WAFProtocol, sev: critical, pl: 1, msg: "Request body in a content encoding the firewall cannot read", reqst: true},

	// ---- 930: path traversal and local file inclusion
	{id: 930100, cat: model.WAFLFI, sev: critical, pl: 1, msg: "Path traversal (../)", in: tPath | tInput | tFile, in2: tHeaders,
		lits: [][]string{oneOf("..")}, re: `(?:^|[\\/])\.\.(?:[\\/;]|$)`},
	{id: 930110, cat: model.WAFLFI, sev: critical, pl: 1, msg: "Path traversal with overlong UTF-8 or Unicode encoding", in: tPath | tArg | tArgName | tCookie, form: fRaw,
		lits: [][]string{oneOf("%c0%a", "%c1%", "%e0%80%a", "%uff0e", "%u2215", "%u2216", "%252e", "%255c")},
		re:   `%c0%a[ef]|%c1%[19]c|%e0%80%a[ef]|%uff0e|%u221[56]|%252e%252e|\.\.%255c`},
	{id: 930120, cat: model.WAFLFI, sev: critical, pl: 1, msg: "Operating system file access attempt", in: tPath | tInput | tFile, in2: tHeaders,
		lits: [][]string{oneOf("etc/", "etc\\", "proc/", "proc\\", "win.ini", "system.ini", "boot.ini", "system32", ".htpasswd", ".htaccess", "web.config", ".ssh", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", ".aws", "wp-config", ".git", "var/log")},
		re:   `(?:^|[\\/\s"'(=:])(?:etc[\\/]+(?:passwd|shadow|group|hosts|issue|sudoers|crontab|master\.passwd)\b|proc[\\/]+(?:self|\d+)[\\/]+(?:environ|cmdline|maps|fd|mem|root|cwd|status)\b|windows[\\/]+(?:win\.ini|system\.ini|system32[\\/]+(?:drivers|config)[\\/])|boot\.ini\b|\.htpasswd\b|\.htaccess\b|web\.config\b|\.ssh[\\/]|id_(?:rsa|dsa|ecdsa|ed25519)\b|\.aws[\\/]+(?:credentials|config)\b|wp-config\.php\b|\.git[\\/]+(?:config|head|index)\b|var[\\/]+log[\\/])`},
	{id: 930130, cat: model.WAFLFI, sev: critical, pl: 1, msg: "Access to a restricted file (source control, credentials, configuration)", in: tPath,
		lits: [][]string{oneOf("/.", "web.config")},
		re:   `/\.(?:env|git|svn|hg|bzr|htaccess|htpasswd|ds_store|aws|ssh|npmrc|yarnrc|dockerenv|docker|idea|vscode|bash_history|mysql_history|psql_history|kube|terraform)(?:[/.]|$)|/web\.config$`},
	{id: 930140, cat: model.WAFLFI, sev: critical, pl: 1, msg: "NTFS alternate data stream in the path", in: tPath | tFile,
		lits: [][]string{oneOf("::$")}, re: `::\$(?:data|index_allocation|bitmap)`},
	{id: 930150, cat: model.WAFLFI, sev: warning, pl: 2, msg: "Windows 8.3 short file name in the path", in: tPath,
		lits: [][]string{oneOf("~")}, re: `~\d(?:[/\\.]|$)`},
	{id: 930160, cat: model.WAFLFI, sev: warning, pl: 2, msg: "Windows device name in the path", in: tPath | tFile,
		lits: [][]string{oneOf("con", "prn", "aux", "nul", "com", "lpt")}, re: `(?:^|[\\/])(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.[^\\/]*)?(?:[\\/]|$)`},

	// ---- 931: remote file inclusion
	{id: 931100, cat: model.WAFRFI, sev: critical, pl: 1, msg: "Remote file inclusion: PHP or stream wrapper URL", in: tInput,
		lits: [][]string{oneOf("://")},
		re:   `(?:^|[^a-z0-9+.-])(?:php|phar|zip|data|expect|glob|compress\.(?:zlib|bzip2)|ogg|rar|ssh2(?:\.[a-z]+)?|gopher|dict|ldap|jar|netdoc|file)://`},
	{id: 931110, cat: model.WAFRFI, sev: critical, pl: 2, msg: "Remote file inclusion: URL ending in ?", in: tArg,
		lits: [][]string{oneOf("://")}, re: `^\s*(?:https?|ftps?)://[^?\s]+\?+\s*$`},
	{id: 931120, cat: model.WAFRFI, sev: critical, pl: 2, msg: "Remote file inclusion: URL with an IP address", in: tArg,
		lits: [][]string{oneOf("://")}, re: `^\s*(?:https?|ftps?)://(?:\d{1,3}\.){3}\d{1,3}(?:[:/?#]|\s*$)`},

	// ---- 932: OS command injection
	{id: 932100, cat: model.WAFRCE, sev: critical, pl: 1, msg: "OS command injection after ; && || or $(", in: tInput, in2: tHeaders,
		lits: [][]string{oneOf(";", "&&", "||", "$(")},
		re:   `(?:;|&&|\|\||\$\()\s*(?:sudo\s+)?(?:(?:/usr)?/s?bin/)?\b` + shellCmds + `\b` + cmdTail},
	{id: 932105, cat: model.WAFRCE, sev: critical, pl: 1, msg: "OS command injection after a pipe or &", in: tInput, in2: tHeaders,
		lits: [][]string{oneOf("|", "&")},
		re:   `(?:\||&)\s*(?:sudo\s+)?(?:(?:/usr)?/s?bin/)?\b(?:bash|sh|zsh|ksh|dash|nc|ncat|netcat|socat|telnet|python[23]?|perl|ruby|php|powershell|pwsh|cmd|whoami|uname|wget|curl|certutil|bitsadmin|base64\s+-d)(?:\.exe)?(?:\s*$|\s*[;&><\x60]|\s+[-/.~$\\\d'"])`},
	{id: 932110, cat: model.WAFRCE, sev: critical, pl: 1, msg: "Windows command injection (cmd, PowerShell, LOLBins)", in: tInput, in2: tHeaders,
		lits: [][]string{oneOf("cmd", "powershell", "pwsh", "iex", "invoke-", "new-object", "download", "certutil", "bitsadmin", "mshta", "regsvr32", "rundll32", "wmic", "frombase64string")},
		re:   `\bcmd(?:\.exe)?\s+/[ckr]\b|\b(?:powershell|pwsh)(?:\.exe)?\s+[-/](?:e|ec|enc|encodedcommand|nop|noprofile|w|windowstyle|c|command|exec|executionpolicy|ep|noni|noninteractive|file)\b|\biex\s*\(|\binvoke-(?:expression|webrequest|restmethod|command|mimikatz|shellcode)\b|\bnew-object\s+(?:-typename\s+)?(?:system\.)?net\.webclient\b|\.download(?:string|file|data)\s*\(|\bcertutil(?:\.exe)?\s+[-/](?:urlcache|decode|encode)\b|\bbitsadmin(?:\.exe)?\s+/transfer\b|\bmshta(?:\.exe)?\s+(?:https?|javascript|vbscript):|\bregsvr32(?:\.exe)?\s+/s\b|\brundll32(?:\.exe)?\s+[^\s,]+,|\bwmic(?:\.exe)?\s+(?:process|os|/node)\b|\[convert\]::frombase64string\s*\(`},
	{id: 932120, cat: model.WAFRCE, sev: critical, pl: 1, msg: "Unix shell expression ($(), ${IFS}, /dev/tcp)", in: tInput, in2: tHeaders,
		lits: [][]string{oneOf("$(", "ifs", "/dev/tcp/", "/dev/udp/")},
		re:   `\$\(\s*` + shellCmds + `\b|\$\{ifs\}|\$ifs\b|/dev/(?:tcp|udp)/\S`},
	{id: 932125, cat: model.WAFRCE, sev: critical, pl: 2, msg: "Unix command substitution in backticks", in: tInput,
		lits: [][]string{oneOf("`")}, re: "\x60\\s*" + shellCmds + "\\b[^\x60]*\x60"},
	{id: 932130, cat: model.WAFRCE, sev: critical, pl: 1, msg: "Shellshock (CVE-2014-6271)", in: tInput | tHeaders | tHeader,
		lits: [][]string{oneOf("()")}, re: `^\s*\(\s*\)\s*\{`},
	{id: 932140, cat: model.WAFRCE, sev: critical, pl: 1, msg: "Unix shell or tool invoked by path", in: tInput | tHeaders,
		lits: [][]string{oneOf("bin/")},
		re:   `(?:^|[^\w.])/(?:usr/)?(?:local/)?s?bin/(?:bash|sh|dash|zsh|ksh|csh|nc|ncat|netcat|socat|python[23]?|perl|ruby|php|wget|curl|chmod|busybox|telnet|id|whoami|cat)\b`},
	{id: 932160, cat: model.WAFRCE, sev: critical, pl: 2, msg: "Upload of a server-side script", in: tFile,
		lits: [][]string{oneOf(".")}, re: `\.(?:aspx?|ashx|asmx|asa|cer|cdx|config|jspx?|cgi|shtml|htaccess)$`},

	// ---- 933: PHP injection
	{id: 933100, cat: model.WAFPHP, sev: critical, pl: 1, msg: "PHP open tag", in: tInput,
		lits: [][]string{oneOf("<?")}, re: `<\?(?:php\b|=)`},
	{id: 933110, cat: model.WAFPHP, sev: critical, pl: 1, msg: "Upload of a PHP script", in: tFile,
		lits: [][]string{oneOf(".ph")}, re: `\.(?:php[3-8]?|phtml|pht|phar|phps)(?:\.|$)`},
	{id: 933120, cat: model.WAFPHP, sev: critical, pl: 1, msg: "PHP configuration directive", in: tInput,
		lits: [][]string{oneOf("allow_url_", "auto_prepend", "auto_append", "disable_functions", "open_basedir", "safe_mode", "register_globals", "enable_dl", "suhosin")},
		re:   `\b(?:allow_url_(?:include|fopen)|auto_(?:prepend|append)_file|disable_functions|open_basedir|safe_mode|register_globals|enable_dl|suhosin\.[\w.]+)\s*=`},
	{id: 933130, cat: model.WAFPHP, sev: critical, pl: 1, msg: "PHP superglobal", in: tInput,
		lits: [][]string{oneOf("$_", "$globals")}, re: `\$_(?:get|post|cookie|request|server|files|env|session)\b|\$globals\s*\[`},
	{id: 933140, cat: model.WAFPHP, sev: critical, pl: 1, msg: "High-risk PHP function call", in: tInput,
		lits: [][]string{oneOf("shell_exec", "passthru", "proc_open", "pcntl_exec", "base64_decode", "gzinflate", "gzuncompress", "str_rot13", "create_function", "call_user_func", "phpinfo", "file_put_contents", "move_uploaded_file")},
		re:   `\b(?:shell_exec|passthru|proc_open|pcntl_exec|base64_decode|gzinflate|gzuncompress|str_rot13|create_function|call_user_func(?:_array)?|phpinfo|file_put_contents|move_uploaded_file)\s*\(`},
	{id: 933145, cat: model.WAFPHP, sev: critical, pl: 2, msg: "PHP code execution function call", in: tInput,
		lits: [][]string{oneOf("system", "exec", "eval", "assert", "popen", "include", "require")},
		re:   `\b(?:system|exec|eval|assert|popen|include|require)(?:_once)?\s*\(\s*[$'"\x60]`},
	{id: 933150, cat: model.WAFPHP, sev: critical, pl: 1, msg: "PHP object injection (serialized object)", in: tInput,
		lits: [][]string{oneOf(`:"`)}, re: `(?:^|[;{}])\s*[oc]:\d+:"[a-z_\\][\w\\]*":\d+:\{`},

	// ---- 934: Node.js / JavaScript
	{id: 934100, cat: model.WAFNode, sev: critical, pl: 1, msg: "Node.js code injection (child_process, require, process)", in: tInput,
		lits: [][]string{oneOf("require", "child_process", "process", "_$$nd_func$$_", "global", "execsync", "spawnsync")},
		re:   `require\s*\(\s*['"\x60](?:node:)?(?:child_process|fs|vm|net|os|process|dgram|cluster|worker_threads|http|https|inspector)['"\x60]\s*\)|\bchild_process\b|\bprocess\s*\.\s*(?:mainmodule|binding|dlopen|kill)\b|_\$\$nd_func\$\$_|\bglobal(?:this)?\s*\.\s*process\b|\.\s*(?:execsync|spawnsync|execfilesync)\s*\(`},
	{id: 934105, cat: model.WAFNode, sev: critical, pl: 2, msg: "Node.js environment access (process.env)", in: tInput,
		lits: [][]string{oneOf("process")}, re: `\bprocess\s*\.\s*env\b`},
	{id: 934110, cat: model.WAFNode, sev: critical, pl: 1, msg: "Prototype pollution (__proto__, constructor.prototype)", in: tInput | tPath,
		lits: [][]string{oneOf("__proto__", "prototype")}, re: `__proto__|\bconstructor\s*(?:\.|\[\s*['"\x60]?|\]\s*\[\s*['"\x60]?)\s*prototype\b`},
	{id: 934120, cat: model.WAFNode, sev: critical, pl: 1, msg: "Server-side request forgery: cloud metadata service", in: tInput, in2: tHeaders,
		lits: [][]string{oneOf("169.254.169.254", "metadata.google", "100.100.100.200", "fd00:ec2", "2852039166", "0xa9fea9fe", "instance-data")},
		re:   `169\.254\.169\.254|metadata\.google\.internal|100\.100\.100\.200|fd00:ec2::254|\b2852039166\b|\b0xa9fea9fe\b|\binstance-data\b`},
	{id: 934130, cat: model.WAFNode, sev: critical, pl: 2, msg: "Server-side request forgery: loopback address", in: tArg,
		lits: [][]string{oneOf("://")}, re: `^\s*(?:https?|gopher|dict|ftp|file)://(?:[^/@]*@)?(?:localhost|127\.\d+\.\d+\.\d+|0\.0\.0\.0|0x7f\w*|2130706433|0177\.[\d.]+|\[::1?\]|\[::ffff:127\.[\d.]+\])(?:[:/?#]|\s*$)`},
	{id: 934140, cat: model.WAFNode, sev: critical, pl: 1, msg: "Server-side template injection", in: tInput,
		lits: [][]string{oneOf("{{", "<%", "#{", "${")},
		re:   `(?s)\{\{.{0,200}?(?:constructor|__proto__|\bprocess\b|\brequire\b|\bglobal\b|this\s*\.|__class__|__globals__|__builtins__|__import__|\.mro\b|__subclasses__|\bconfig\s*\.|\bself\s*\.\s*_|\blipsum\b|\bcycler\b|\bjoiner\b|request\s*\.\s*application|\|\s*attr\s*\()|<%[=-]?\s*(?:process|require|global)\b|#\{.{0,200}?\b(?:process|require|global)\b|\$\{.{0,200}?(?:process\s*\.|require\s*\(|constructor\s*\()`},
	{id: 934150, cat: model.WAFNode, sev: errSev, pl: 1, msg: "Template injection probe ({{7*7}})", in: tInput,
		lits: [][]string{oneOf("{{", "${", "<%", "#{")},
		re:   `\{\{\s*\d+\s*[*+]\s*['"]?\d+['"]?\s*\}\}|\$\{\s*\d+\s*\*\s*\d+\s*\}|<%=\s*\d+\s*\*\s*\d+\s*%>|#\{\s*\d+\s*\*\s*\d+\s*\}`},
	{id: 934160, cat: model.WAFNode, sev: critical, pl: 1, msg: "JavaScript sandbox escape (constructor.constructor)", in: tInput,
		lits: [][]string{oneOf("constructor", "function", "import")},
		re:   `constructor\s*\.\s*constructor\s*\(|\[\s*['"\x60]constructor['"\x60]\s*\]|\bfunction\s*\(\s*\)\s*\{\s*return\s+(?:this|process|global)\b|\bnew\s+function\s*\(|\bimport\s*\(\s*['"\x60](?:node:)?(?:child_process|fs)\b`},

	// ---- 941: cross-site scripting
	{id: 941100, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: <script> tag", in: tPath | tInput | tHeaders | tFile,
		lits: [][]string{oneOf("<script")}, re: `<script\b`},
	{id: 941110, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: event handler in an HTML tag", in: tPath | tInput | tHeaders | tFile,
		lits: [][]string{oneOf("<"), oneOf("on"), oneOf("=")}, re: `<[a-z][a-z0-9:-]*(?:/+|\s+|\s[^>]*?[\s"'\x60])on[a-z]{3,40}\s*=`},
	{id: 941120, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: attribute injection with an event handler", in: tInput,
		lits: [][]string{oneOf("on"), oneOf("=")}, re: `(?:^|["'\x60;])\s*on` + eventHandlers + `\s*=`},
	{id: 941125, cat: model.WAFXSS, sev: critical, pl: 2, msg: "XSS: event handler attribute", in: tInput | tHeaders,
		lits: [][]string{oneOf("on"), oneOf("=")}, re: `(?:^|[\s/])on` + eventHandlers + `\s*=`},
	{id: 941130, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: javascript:, vbscript: or data:text/html URI", in: tPath | tInput | tHeaders,
		lits: [][]string{oneOf("script", "data:")},
		re:   `(?:java|vb|live)script\s*:\s*(?:[\w$.\[\]'"]+\s*[(=\x60]|/|%|&|'|"|\[|\(|\{)|\bdata:\s*text/html\b`},
	{id: 941135, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: obfuscated javascript: URI", in: tPath | tInput,
		lits: [][]string{oneOf("\t", "\n", "\r")}, re: `j[\t\n\r]*a[\t\n\r]*v[\t\n\r]*a[\t\n\r]*s[\t\n\r]*c[\t\n\r]*r[\t\n\r]*i[\t\n\r]*p[\t\n\r]*t[\t\n\r]*:`},
	{id: 941140, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: dangerous HTML tag (iframe, object, embed, meta, base)", in: tPath | tInput | tHeaders,
		lits: [][]string{oneOf("<"), oneOf("frame", "object", "embed", "applet", "meta", "base", "link", "isindex", "xss", "import", "portal")},
		re:   `<(?:iframe|frame|frameset|object|embed|applet|meta|base|link|isindex|xss|vmlframe|import|portal)\b`},
	{id: 941150, cat: model.WAFXSS, sev: critical, pl: 1, msg: "XSS: JavaScript sink or payload (document.cookie, alert(), eval())", in: tInput | tHeaders,
		lits: [][]string{oneOf("document", "window.", "window .", "innerhtml", "outerhtml", "eval", "fromcharcode", "alert", "prompt", "confirm", "settimeout", "setinterval")},
		re:   `\bdocument\s*\.\s*(?:cookie|write(?:ln)?|domain|location)\b|\bwindow\s*\.\s*(?:location|open)\s*[=(]|\.\s*(?:inner|outer)html\s*=|\beval\s*\(\s*(?:atob|unescape|decodeuri\w*|string\s*\.\s*fromcharcode|['"\x60])|\bstring\s*\.\s*fromcharcode\s*\(|\b(?:alert|prompt|confirm)\s*(?:\(\s*(?:\d|['"\x60/]|document|window|this|\))|\x60)|\bset(?:timeout|interval)\s*\(\s*['"\x60]`},
	{id: 941160, cat: model.WAFXSS, sev: critical, pl: 2, msg: "XSS: HTML tag injection", in: tInput | tPath,
		lits: [][]string{oneOf("<")},
		re:   `<(?:svg|math|img|image|video|audio|source|body|input|details|marquee|style|form|button|textarea|select|keygen|bgsound|template|animate|set|dialog|object)\b[^>]*>?`},
	{id: 941170, cat: model.WAFXSS, sev: errSev, pl: 2, msg: "XSS: CSS expression or binding", in: tInput,
		lits: [][]string{oneOf("expression", "-moz-binding", "behavior", "@import", "url(")},
		re:   `\bexpression\s*\(|-moz-binding\s*:|\bbehavior\s*:\s*url\s*\(|@import\b|url\s*\(\s*['"]?\s*javascript:`},
	{id: 941200, cat: model.WAFXSS, sev: warning, pl: 3, msg: "HTML markup", in: tInput,
		lits: [][]string{oneOf("<")}, re: `<[a-z!/?][a-z0-9-]*`},

	// ---- 942: SQL and NoSQL injection
	{id: 942100, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: quote followed by a boolean comparison (' OR 'a'='a)", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("'", `"`, "`", ")"), oneOf("or", "and", "xor", "||", "&&")},
		re:   `['"\x60)]\s*(?:or|and|xor|\|\||&&)\s*\(*\s*['"\x60]?[\w.@$-]*['"\x60]?\s*(?:=|<>|!=|<=>|<=?|>=?|\blike\b|\bregexp\b|\brlike\b|\bsounds\s+like\b|\bis\s+(?:not\s+)?null\b|\bbetween\b|\bin\s*\()`},
	{id: 942110, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: quote followed by a boolean and a comment (' OR 1--)", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("'", `"`, "`", ")"), oneOf("or", "and", "xor", "||", "&&")},
		re:   `['"\x60)]\s*(?:or|and|xor|\|\||&&)\s*(?:true|false|null|not\s+false|\d+)\s*(?:--|#|/\*|;|\)|$)`},
	{id: 942120, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: tautology (OR 1=1)", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("or", "and", "xor"), oneOf("=", "<", ">", "like")},
		re:   `(?:^|[\s(\d'"\x60])(?:or|and|xor)\s+\(*\s*(?:\d+|'[^']*'|"[^"]*")\s*\)*\s*(?:=|<>|!=|<=?|>=?|\blike\b)\s*\(*\s*(?:\d+|'[^']*'|"[^"]*")`},
	{id: 942130, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: UNION SELECT", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("union"), oneOf("select")}, re: `\bunion\s*(?:all\s+|distinct\s+)?\(?\s*select\b`},
	{id: 942140, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: stacked query (; DROP TABLE)", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf(";")},
		re:   `(?:^|['"\x60)\d])\s*;\s*(?:drop\s+(?:table|database|schema|view|index|user|procedure|function)\b|truncate\s+table\b|alter\s+(?:table|database|user|login)\b|insert\s+into\b|delete\s+from\b|update\s+[\w.\x60"\[\]]+\s+set\b|exec(?:ute)?\s*(?:\(|@|xp_|sp_|master\b)|declare\s+@|shutdown\b|create\s+(?:table|database|user|login|procedure|function|trigger)\b|grant\s+\w|waitfor\s+(?:delay|time)\b|select\s)`},
	{id: 942150, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: SELECT statement", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("select"), oneOf("from")},
		re:   `(?s)\bselect\s+(?:\*|count\s*\(|@@\w+|null\s*,|\d+\s*,\s*\d+|(?:user|version|database|schema|current_user|system_user)\s*\(\s*\)|(?:group_)?concat(?:_ws)?\s*\(|top\s+\d+\s)(?:.{0,200}?)\bfrom\b`},
	{id: 942155, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: INSERT, UPDATE or DELETE statement", in: tInput, form: fSQL,
		lits: [][]string{oneOf("insert", "update", "delete")},
		re:   `\binsert\s+into\s+[\w.\x60"\[\]]+\s*(?:\([^)]*\)\s*)?(?:values|select)\b|\bupdate\s+[\w.\x60"\[\]]+\s+set\s+[\w.\x60"\[\]]+\s*=|\bdelete\s+from\s+[\w.\x60"\[\]]+\s+where\s`},
	{id: 942160, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: time-based blind (SLEEP, WAITFOR DELAY)", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("sleep", "benchmark", "waitfor", "dbms_")},
		re:   `\b(?:pg_)?sleep\s*\(\s*\d|\bbenchmark\s*\(\s*\d|\bwaitfor\s+(?:delay|time)\s+['"]|\bdbms_(?:pipe\s*\.\s*receive_message|lock\s*\.\s*sleep)\s*\(`},
	{id: 942170, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: database schema or version probing", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("information_schema", "mysql.", "sys.", "sysobjects", "syscolumns", "sysusers", "sysdatabases", "pg_", "sqlite_", "all_tables", "user_tables", "@@")},
		re:   `\binformation_schema\b|\bmysql\s*\.\s*(?:user|db)\b|\bsys\s*\.\s*(?:objects|columns|tables|databases|sql_logins|schemas)\b|\bsys(?:objects|columns|users|databases)\b|\bpg_(?:catalog|shadow|user|tables|database|namespace)\b|\bsqlite_(?:master|schema|temp_master)\b|\ball_tables\b|\buser_tables\b|@@(?:version|datadir|hostname|basedir|servername|servicename)\b`},
	{id: 942180, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: file access or command execution from SQL", in: tInput, in2: tHeaders, form: fSQL,
		lits: [][]string{oneOf("load_file", "outfile", "dumpfile", "extractvalue", "updatexml", "xp_", "sp_", "master", "openrowset", "opendatasource", "utl_", "program", "char")},
		re:   `\bload_file\s*\(|\binto\s+(?:out|dump)file\b|\b(?:extractvalue|updatexml)\s*\(|\bxp_(?:cmdshell|regread|regwrite|dirtree|fileexist|servicecontrol)\b|\bsp_(?:executesql|oacreate|oamethod|makewebtask|addextendedproc|configure|password)\b|\bexec(?:ute)?\s+master\s*\.|\bopen(?:rowset|datasource)\s*\(|\butl_(?:http|inaddr|file|tcp)\b|\bcopy\s+[\w.]+\s+(?:from|to)\s+program\b|\bchar\s*\(\s*\d+\s*(?:,\s*\d+\s*){2,}\)`},
	{id: 942190, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: comment after a closing quote (admin'--)", in: tInput,
		lits: [][]string{oneOf("'", `"`, "`"), oneOf("--", "#", "/*")},
		re:   `^[\w@.+-]*['"\x60]\s*\)*\s*(?:--|#|/\*)`},
	{id: 942195, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "SQL injection: MySQL versioned comment (/*!)", in: tInput,
		lits: [][]string{oneOf("/*!")}, re: `/\*!\d{0,5}\s*[a-z(]`},
	{id: 942200, cat: model.WAFSQLi, sev: critical, pl: 2, msg: "SQL injection: string concatenation or hex literal", in: tInput, form: fSQL,
		lits: [][]string{oneOf("'", `"`, "0x", "char")},
		re:   `['"]\s*(?:\|\||\+)\s*['"]|\b0x[0-9a-f]{16,}\b|\bchar\s*\(\s*\d+\s*\)\s*(?:\+|\|\|)`},
	{id: 942210, cat: model.WAFSQLi, sev: critical, pl: 1, msg: "NoSQL injection: MongoDB operator in an argument name", in: tArgName,
		lits: [][]string{oneOf("$")},
		re:   `(?:^|[\[.])\$(?:ne|eq|gt|gte|lt|lte|in|nin|regex|where|exists|expr|or|and|nor|not|elemmatch|function|accumulator|size|all|type|jsonschema|text|mod|lookup)(?:\]|$|\.)`},
	{id: 942220, cat: model.WAFSQLi, sev: errSev, pl: 2, msg: "SQL keywords", in: tInput, form: fSQL,
		lits: [][]string{oneOf("select", "union", "insert", "update", "delete", "drop", "alter", "exec", "declare", "truncate")},
		re:   `(?s)\b(?:select|union|insert|update|delete|drop|alter|exec|declare|truncate)\b.{0,100}?\b(?:from|into|table|where|set|values)\b`},
	{id: 942230, cat: model.WAFSQLi, sev: critical, pl: 2, msg: "SQL injection: SELECT columns FROM", in: tInput, form: fSQL,
		lits: [][]string{oneOf("select"), oneOf("from")},
		re:   `\bselect\s+[\w.\x60"\[\]]+(?:\s*,\s*[\w.\x60"\[\]]+)+\s+from\b`},
	{id: 942240, cat: model.WAFSQLi, sev: critical, pl: 2, msg: "SQL injection: DROP or TRUNCATE", in: tInput, form: fSQL,
		lits: [][]string{oneOf("drop", "truncate")},
		re:   `\b(?:drop\s+(?:table|database|schema)|truncate\s+table)\s+(?:if\s+exists\s+)?[\w.\x60"\[\]]+`},
	{id: 942300, cat: model.WAFSQLi, sev: warning, pl: 3, msg: "SQL comment sequence", in: tArg | tCookie,
		lits: [][]string{oneOf("--", "#", "/*")}, re: `--|#|/\*`},

	// ---- 944: Java
	{id: 944100, cat: model.WAFJava, sev: critical, pl: 1, msg: "Log4Shell JNDI lookup (CVE-2021-44228)", in: tAll,
		lits: [][]string{oneOf("${")}, fn: log4shell},
	{id: 944105, cat: model.WAFJava, sev: critical, pl: 1, msg: "Nested ${} lookups (Log4Shell obfuscation)", in: tAll,
		lits: [][]string{oneOf("${")}, re: `\$\{[^{}]{0,64}\$\{`},
	{id: 944110, cat: model.WAFJava, sev: critical, pl: 1, msg: "Java code execution (Runtime, ProcessBuilder, deserialization gadgets)", in: tInput, in2: tHeaders,
		lits: [][]string{oneOf("java.", "javax.", "getruntime", "org.apache.commons.collections", "com.sun.org")},
		re:   `\bjava\s*\.\s*lang\s*\.\s*(?:runtime|processbuilder|reflect\s*\.|classloader|system\b)|\.\s*getruntime\s*\(\s*\)|\bjavax\s*\.\s*script\s*\.\s*scriptenginemanager\b|\bjava\s*\.\s*io\s*\.\s*objectinputstream\b|\borg\s*\.\s*apache\s*\.\s*commons\s*\.\s*collections\d?\s*\.\s*functors\b|\bcom\s*\.\s*sun\s*\.\s*org\s*\.\s*apache\s*\.\s*xalan\b|\bjava\s*\.\s*beans\s*\.\s*xmldecoder\b|\bjavax\s*\.\s*naming\s*\.\s*initialcontext\b`},
	{id: 944120, cat: model.WAFJava, sev: critical, pl: 1, msg: "Serialized Java object", in: tInput, form: fDecoded,
		fn: func(s string) (string, bool) {
			t := strings.TrimSpace(s)
			for _, p := range []string{"\xac\xed\x00\x05", "rO0AB", "aced0005", "ACED0005"} {
				if strings.HasPrefix(t, p) {
					return t[:min(len(t), 16)], true
				}
			}
			return "", false
		}},
	{id: 944130, cat: model.WAFJava, sev: critical, pl: 1, msg: "Spring4Shell class loader manipulation (CVE-2022-22965)", in: tArgName,
		lits: [][]string{oneOf("classloader", "class[")}, re: `\bclass\s*(?:\.|\[\s*['"]?)\s*module\b|\bclass\s*\.\s*classloader\b|\bclassloader\s*\.\s*resources\b`},
	{id: 944140, cat: model.WAFJava, sev: critical, pl: 1, msg: "Java expression language or OGNL injection", in: tAll,
		lits: [][]string{oneOf("%{", "${", "#_memberaccess", "@ognl", "#context", "java.", "(#")},
		re:   `%\{\s*[#@(]|\$\{\s*(?:#|@|t\s*\()|#_memberaccess\b|@ognl\b|#context\s*\[|\bnew\s+java\s*\.|@java\s*\.\s*lang\b|\(\s*#[a-z_]+\s*=`},
}

// controlChars finds ASCII control characters; with text, tab and line
// breaks are allowed (text areas).
func controlChars(text bool) func(string) (string, bool) {
	return func(s string) (string, bool) {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == 0 || (c >= 0x20 && c != 0x7f) {
				continue // NUL has its own rule
			}
			if text && (c == '\t' || c == '\n' || c == '\r') {
				continue
			}
			return s[max(0, i-20):min(len(s), i+20)], true
		}
		return "", false
	}
}

// Log4j lookups that obfuscate ${jndi:...}: ${lower:j}, ${::-j},
// ${env:NOPE:-j}, ${date:'j'}. Each round replaces the innermost ones by
// the text they produce.
var (
	log4jLookup = regexp.MustCompile(`\$\{(?:(?:lower|upper|date|env|sys|main|bundle|ctx|marker|sd|k8s|docker|spring|web|java|log4j|base64|jvmrunargs|map|event)\s*:)?(?:[^${}]*?:-)?'?([^${}']*)'?\}`)
	log4jJNDI   = regexp.MustCompile(`\$\{\s*jndi\s*:`)
)

func log4shell(s string) (string, bool) {
	i := strings.Index(s, "${")
	if i < 0 {
		return "", false
	}
	t := s[i:min(len(s), i+4096)]
	for round := 0; round < 8; round++ {
		if loc := log4jJNDI.FindStringIndex(t); loc != nil {
			return t[loc[0]:min(len(t), loc[1]+40)], true
		}
		u := log4jLookup.ReplaceAllString(t, "$1")
		if u == t {
			break
		}
		t = u
	}
	return "", false
}

var (
	rules    []*rule
	byID     map[int]*rule
	matcher  *automaton
	catIndex = map[string]int{}
)

func init() {
	for i, c := range model.WAFCategories {
		catIndex[c] = i
	}
	var lits litIndex
	byID = map[int]*rule{}
	for i, d := range defs {
		c, ok := catIndex[d.cat]
		if !ok {
			panic(fmt.Sprintf("waf: rule %d: unknown category %q", d.id, d.cat))
		}
		r := &rule{
			WAFRuleInfo: model.WAFRuleInfo{ID: d.id, Category: d.cat, Severity: severityName(d.sev), Score: d.sev, Paranoia: d.pl, Message: d.msg},
			idx:         i, cat: c, in: d.in, in2: d.in2, form: d.form, fn: d.fn, request: d.reqst,
		}
		if d.re != "" {
			r.re = regexp.MustCompile(d.re)
		}
		if !r.request && r.re == nil && r.fn == nil {
			panic(fmt.Sprintf("waf: rule %d matches nothing", d.id))
		}
		for _, g := range d.lits {
			var set litSet
			for _, l := range g {
				if l != strings.ToLower(l) && d.form != fDecoded {
					panic(fmt.Sprintf("waf: rule %d: literal %q is not lowercase", d.id, l))
				}
				set.add(lits.id(l))
			}
			r.groups = append(r.groups, set)
		}
		if _, dup := byID[d.id]; dup {
			panic(fmt.Sprintf("waf: duplicate rule %d", d.id))
		}
		byID[d.id] = r
		rules = append(rules, r)
	}
	matcher = buildAutomaton(lits.lits)
}

// Rules returns the rule catalog, by ID.
func Rules() []model.WAFRuleInfo {
	out := make([]model.WAFRuleInfo, len(rules))
	for i, r := range rules {
		out[i] = r.WAFRuleInfo
	}
	slices.SortFunc(out, func(a, b model.WAFRuleInfo) int { return a.ID - b.ID })
	return out
}

// Rule returns a rule's description, and whether there is one.
func Rule(id int) (model.WAFRuleInfo, bool) {
	r, ok := byID[id]
	if !ok {
		return model.WAFRuleInfo{}, false
	}
	return r.WAFRuleInfo, true
}
