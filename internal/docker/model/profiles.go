package model

const (
	DNSProfileSystem           = "system"
	DNSProfilePublicCloudflare = "public-cloudflare"
	DNSProfilePublicGoogle     = "public-google"
	DNSProfileDoHCloudflare    = "doh-cloudflare"
	DNSProfileDoHGoogle        = "doh-google"
	DNSProfileCloudflare       = "cloudflare"
	DNSProfileGoogle           = "google"
	OutboundIPPreferIPv4       = "prefer-ipv4"
	OutboundIPPreferIPv6       = "prefer-ipv6"
	OutboundIPIPv4Only         = "ipv4-only"
	OutboundIPIPv6Only         = "ipv6-only"
	OutboundIPUnset            = "unset"
)
