package tools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func matchDomainPattern(hostname string, patterns []string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if pattern == hostname {
			return true
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(hostname, suffix) && hostname != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}

func validateDomainPatterns(patterns []string) error {
	for _, pattern := range patterns {
		if err := validateDomainPattern(pattern); err != nil {
			return fmt.Errorf("%q: %w", pattern, err)
		}
	}
	return nil
}

func validateDomainPattern(pattern string) error {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return fmt.Errorf("empty domain pattern")
	}
	if strings.HasPrefix(pattern, "*.") {
		pattern = strings.TrimPrefix(pattern, "*.")
	}
	if strings.ContainsAny(pattern, "/: ") {
		return fmt.Errorf("must be a bare hostname")
	}
	parts := strings.Split(pattern, ".")
	if len(parts) < 2 {
		return fmt.Errorf("must include at least one dot")
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("contains an empty label")
		}
	}
	return nil
}

func validateRemoteURL(rawURL string, allowPrivate bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are supported")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("missing hostname in URL")
	}
	if allowPrivate {
		return parsed, nil
	}
	if err := disallowPrivateHost(parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func disallowPrivateHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrLocalIP(ip) {
			return fmt.Errorf("private or local address %s is not allowed", host)
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("localhost is not allowed")
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host %s: %w", host, err)
	}
	for _, addr := range addrs {
		if isPrivateOrLocalIP(addr) {
			return fmt.Errorf("private or local address %s is not allowed", addr.String())
		}
	}
	return nil
}

func isPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 10:
			return true
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return true
		case v4[0] == 192 && v4[1] == 168:
			return true
		case v4[0] == 169 && v4[1] == 254:
			return true
		case v4[0] == 127:
			return true
		}
		return false
	}
	return ip.IsPrivate()
}
