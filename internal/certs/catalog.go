package certs

import (
	"fmt"
	"os"
	"sync"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/azuredns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/cloudns"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/dnsimple"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/providers/dns/exec"
	"github.com/go-acme/lego/v4/providers/dns/gandiv5"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/hetzner"
	"github.com/go-acme/lego/v4/providers/dns/httpreq"
	"github.com/go-acme/lego/v4/providers/dns/ionos"
	"github.com/go-acme/lego/v4/providers/dns/linode"
	"github.com/go-acme/lego/v4/providers/dns/namecheap"
	"github.com/go-acme/lego/v4/providers/dns/namesilo"
	"github.com/go-acme/lego/v4/providers/dns/netcup"
	"github.com/go-acme/lego/v4/providers/dns/ovh"
	"github.com/go-acme/lego/v4/providers/dns/porkbun"
	"github.com/go-acme/lego/v4/providers/dns/rfc2136"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"github.com/go-acme/lego/v4/providers/dns/vultr"
)

type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Optional bool   `json:"optional"`
}

type ProviderInfo struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`

	build func() (challenge.Provider, error)
}

func f(key, label string) Field        { return Field{Key: key, Label: label} }
func secret(key, label string) Field   { return Field{Key: key, Label: label, Secret: true} }
func optional(key, label string) Field { return Field{Key: key, Label: label, Optional: true} }
func optSecret(key, label string) Field {
	return Field{Key: key, Label: label, Secret: true, Optional: true}
}

// Catalog lists the DNS providers available for DNS-01 validation (needed
// for wildcard certificates, or when port 80 is not reachable). Credentials
// use lego's environment variable names.
var Catalog = []ProviderInfo{
	{Code: "cloudflare", Name: "Cloudflare", Fields: []Field{secret("CLOUDFLARE_DNS_API_TOKEN", "API token (Zone:DNS:Edit)"), optSecret("CLOUDFLARE_ZONE_API_TOKEN", "Zone read token")},
		build: func() (challenge.Provider, error) { return cloudflare.NewDNSProvider() }},
	{Code: "route53", Name: "Amazon Route 53", Fields: []Field{f("AWS_ACCESS_KEY_ID", "Access key ID"), secret("AWS_SECRET_ACCESS_KEY", "Secret access key"), f("AWS_REGION", "Region"), optional("AWS_HOSTED_ZONE_ID", "Hosted zone ID")},
		build: func() (challenge.Provider, error) { return route53.NewDNSProvider() }},
	{Code: "azuredns", Name: "Azure DNS", Fields: []Field{f("AZURE_TENANT_ID", "Tenant ID"), f("AZURE_CLIENT_ID", "Client ID"), secret("AZURE_CLIENT_SECRET", "Client secret"), f("AZURE_SUBSCRIPTION_ID", "Subscription ID"), f("AZURE_RESOURCE_GROUP", "Resource group"), optional("AZURE_ZONE_NAME", "Zone name")},
		build: func() (challenge.Provider, error) { return azuredns.NewDNSProvider() }},
	{Code: "rfc2136", Name: "RFC 2136 / Windows DNS Server (dynamic update)", Fields: []Field{
		f("DNSUPDATE_NAMESERVER", "Name server (host:port)"),
		optional("DNSUPDATE_TSIG_KEY", "TSIG key name"), optSecret("DNSUPDATE_TSIG_SECRET", "TSIG secret"), optional("DNSUPDATE_TSIG_ALGORITHM", "TSIG algorithm (e.g. hmac-sha256.)"),
		optional("DNSUPDATE_TSIG_GSS_REALM", "GSS-TSIG realm (Active Directory domain)"), optional("DNSUPDATE_TSIG_GSS_USERNAME", "GSS-TSIG user"), optSecret("DNSUPDATE_TSIG_GSS_PASSWORD", "GSS-TSIG password")},
		build: func() (challenge.Provider, error) { return rfc2136.NewDNSProvider() }},
	{Code: "digitalocean", Name: "DigitalOcean", Fields: []Field{secret("DO_AUTH_TOKEN", "API token")},
		build: func() (challenge.Provider, error) { return digitalocean.NewDNSProvider() }},
	{Code: "godaddy", Name: "GoDaddy", Fields: []Field{f("GODADDY_API_KEY", "API key"), secret("GODADDY_API_SECRET", "API secret")},
		build: func() (challenge.Provider, error) { return godaddy.NewDNSProvider() }},
	{Code: "namecheap", Name: "Namecheap", Fields: []Field{f("NAMECHEAP_API_USER", "API user"), secret("NAMECHEAP_API_KEY", "API key")},
		build: func() (challenge.Provider, error) { return namecheap.NewDNSProvider() }},
	{Code: "hetzner", Name: "Hetzner", Fields: []Field{secret("HETZNER_API_TOKEN", "API token")},
		build: func() (challenge.Provider, error) { return hetzner.NewDNSProvider() }},
	{Code: "ovh", Name: "OVHcloud", Fields: []Field{f("OVH_ENDPOINT", "Endpoint (ovh-eu, ovh-ca...)"), f("OVH_APPLICATION_KEY", "Application key"), secret("OVH_APPLICATION_SECRET", "Application secret"), secret("OVH_CONSUMER_KEY", "Consumer key")},
		build: func() (challenge.Provider, error) { return ovh.NewDNSProvider() }},
	{Code: "gandiv5", Name: "Gandi", Fields: []Field{secret("GANDIV5_PERSONAL_ACCESS_TOKEN", "Personal access token")},
		build: func() (challenge.Provider, error) { return gandiv5.NewDNSProvider() }},
	{Code: "porkbun", Name: "Porkbun", Fields: []Field{f("PORKBUN_API_KEY", "API key"), secret("PORKBUN_SECRET_API_KEY", "Secret API key")},
		build: func() (challenge.Provider, error) { return porkbun.NewDNSProvider() }},
	{Code: "linode", Name: "Linode / Akamai", Fields: []Field{secret("LINODE_TOKEN", "API token")},
		build: func() (challenge.Provider, error) { return linode.NewDNSProvider() }},
	{Code: "vultr", Name: "Vultr", Fields: []Field{secret("VULTR_API_KEY", "API key")},
		build: func() (challenge.Provider, error) { return vultr.NewDNSProvider() }},
	{Code: "dnsimple", Name: "DNSimple", Fields: []Field{secret("DNSIMPLE_OAUTH_TOKEN", "OAuth token")},
		build: func() (challenge.Provider, error) { return dnsimple.NewDNSProvider() }},
	{Code: "namesilo", Name: "NameSilo", Fields: []Field{secret("NAMESILO_API_KEY", "API key")},
		build: func() (challenge.Provider, error) { return namesilo.NewDNSProvider() }},
	{Code: "ionos", Name: "IONOS", Fields: []Field{secret("IONOS_API_KEY", "API key (prefix.secret)")},
		build: func() (challenge.Provider, error) { return ionos.NewDNSProvider() }},
	{Code: "netcup", Name: "netcup", Fields: []Field{f("NETCUP_CUSTOMER_NUMBER", "Customer number"), f("NETCUP_API_KEY", "API key"), secret("NETCUP_API_PASSWORD", "API password")},
		build: func() (challenge.Provider, error) { return netcup.NewDNSProvider() }},
	{Code: "cloudns", Name: "ClouDNS", Fields: []Field{f("CLOUDNS_AUTH_ID", "Auth ID"), secret("CLOUDNS_AUTH_PASSWORD", "Auth password")},
		build: func() (challenge.Provider, error) { return cloudns.NewDNSProvider() }},
	{Code: "duckdns", Name: "Duck DNS", Fields: []Field{secret("DUCKDNS_TOKEN", "Token")},
		build: func() (challenge.Provider, error) { return duckdns.NewDNSProvider() }},
	{Code: "httpreq", Name: "HTTP request (custom endpoint)", Fields: []Field{f("HTTPREQ_ENDPOINT", "Endpoint URL"), optional("HTTPREQ_USERNAME", "Username"), optSecret("HTTPREQ_PASSWORD", "Password"), optional("HTTPREQ_MODE", "Mode (RAW for raw values)")},
		build: func() (challenge.Provider, error) { return httpreq.NewDNSProvider() }},
	{Code: "exec", Name: "External program", Fields: []Field{f("EXEC_PATH", "Program path"), optional("EXEC_MODE", "Mode (RAW)")},
		build: func() (challenge.Provider, error) { return exec.NewDNSProvider() }},
}

func providerInfo(code string) (ProviderInfo, bool) {
	for _, p := range Catalog {
		if p.Code == code {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// lego providers read their credentials from the environment when they are
// constructed. Constructing one with a given set of credentials therefore
// means setting them, building the provider and restoring the environment,
// all under one lock so concurrent issuances cannot see each other's keys.
var envMu sync.Mutex

func buildDNSProvider(code string, creds map[string]string) (challenge.Provider, error) {
	info, ok := providerInfo(code)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider %q", code)
	}
	for _, fl := range info.Fields {
		if !fl.Optional && creds[fl.Key] == "" {
			return nil, fmt.Errorf("DNS provider %s: %s is required", info.Name, fl.Label)
		}
	}
	envMu.Lock()
	defer envMu.Unlock()
	saved := map[string]*string{}
	for k, v := range creds {
		if old, ok := os.LookupEnv(k); ok {
			saved[k] = &old
		} else {
			saved[k] = nil
		}
		if v != "" {
			os.Setenv(k, v)
		} else {
			os.Unsetenv(k)
		}
	}
	defer func() {
		for k, v := range saved {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}()
	return info.build()
}
