package permission

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"

	"github.com/keakon/chord/internal/toolname"
)

// webFetchTarget is a parsed WebFetch URL used for network-aware rule matching.
type webFetchTarget struct {
	host string // lowercased hostname (no brackets for IPv6)
	ip   net.IP // non-nil when host is a literal IP
	port int    // effective port (explicit, or scheme default); -1 when unknown
}

// EvaluateWebFetch resolves the action for a WebFetch call to rawURL using
// network-aware pattern matching: a rule pattern targets a host (domain glob,
// literal IP, or CIDR) and an optional port (single or lo-hi range). Matching
// keeps the standard last-match-wins ordering; the tool-name side still uses
// glob so `*` and `web_*` rules apply. Returns an unfound MatchResult when
// nothing matches.
//
// Enforcement is pre-fetch only: the URL the model supplied is matched as-is.
// A domain that resolves to an internal address, or a redirect to one, is not
// re-checked against the resolved IP.
func (rs Ruleset) EvaluateWebFetch(rawURL string) MatchResult {
	target, parsed := parseWebFetchTarget(rawURL)
	for _, r := range slices.Backward(rs) {

		if !globMatch(toolname.WebFetch, toolname.Normalize(r.Permission)) {
			continue
		}
		if strings.TrimSpace(r.Pattern) == "*" || parsed && matchWebFetchPattern(r.Pattern, target) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

func parseWebFetchTarget(rawURL string) (webFetchTarget, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return webFetchTarget{}, false
	}
	host, ok := canonicalWebFetchDomain(u.Hostname())
	if !ok {
		return webFetchTarget{}, false
	}
	port := -1
	if p := u.Port(); p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || !validWebFetchPort(n) {
			return webFetchTarget{}, false
		}
		port = n
	} else {
		switch u.Scheme {
		case "http", "ws":
			port = 80
		case "https", "wss":
			port = 443
		}
	}
	return webFetchTarget{
		host: host,
		ip:   net.ParseIP(host),
		port: port,
	}, true
}

func canonicalWebFetchDomain(host string) (string, bool) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return "", false
	}
	if net.ParseIP(host) != nil {
		return host, true
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", false
	}
	ascii = strings.TrimSuffix(strings.ToLower(ascii), ".")
	return ascii, ascii != ""
}

func matchWebFetchPattern(pattern string, target webFetchTarget) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.Contains(pattern, "://") {
		return false
	}
	host, port, ok := parseWebFetchPatternHostPort(pattern)
	if !ok {
		return false
	}
	return matchWebFetchHost(host, target) &&
		matchWebFetchPort(port, target.port)
}

// parseWebFetchPatternHostPort splits "host[:port]" into host and port. It
// understands bracketed IPv6 and IPv6 CIDR (`[fd00::/8]:443`), bare CIDR and
// bare IPs (which carry no port), and `host:port` for domains and IPv4.
func parseWebFetchPatternHostPort(rest string) (host, port string, ok bool) {
	if rest == "" {
		return "", "", false
	}
	if strings.HasPrefix(rest, "[") {
		close := strings.Index(rest, "]")
		if close < 0 {
			return "", "", false
		}
		host = rest[1:close]
		after := rest[close+1:]
		switch {
		case after == "":
			return host, "", validWebFetchPatternHost(host)
		case strings.HasPrefix(after, ":") && len(after) > 1:
			return host, after[1:], validWebFetchPatternHost(host)
		default:
			return "", "", false
		}
	}
	if _, _, err := net.ParseCIDR(rest); err == nil {
		return rest, "", true
	}
	if net.ParseIP(rest) != nil { // bare IPv4/IPv6, no port
		return rest, "", true
	}
	if strings.Count(rest, ":") == 1 {
		host, port, _ = strings.Cut(rest, ":")
		if validWebFetchPatternHost(host) && port != "" {
			return host, port, true
		}
		return "", "", false
	}
	if strings.Contains(rest, "/") || strings.Contains(rest, ":") {
		return "", "", false
	}
	return rest, "", validWebFetchPatternHost(rest)
}

func validWebFetchPatternHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(host); err == nil {
		return true
	}
	return !strings.ContainsAny(host, "/:[]")
}

func matchWebFetchHost(patternHost string, target webFetchTarget) bool {
	if _, cidr, err := net.ParseCIDR(patternHost); err == nil {
		return target.ip != nil && cidr.Contains(target.ip)
	}
	if pip := net.ParseIP(patternHost); pip != nil {
		return target.ip != nil && pip.Equal(target.ip)
	}
	patternHost, ok := canonicalWebFetchPatternDomain(patternHost)
	if !ok {
		return false
	}
	targetHost, err := idna.Lookup.ToUnicode(target.host)
	if err != nil {
		return false
	}
	return globMatch(strings.ToLower(norm.NFC.String(targetHost)), patternHost)
}

func canonicalWebFetchPatternDomain(host string) (string, bool) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return "", false
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if label == "" {
			return "", false
		}
		if strings.ContainsAny(label, "*?") {
			labels[i] = strings.ToLower(norm.NFC.String(label))
			continue
		}
		ascii, err := idna.Lookup.ToASCII(label)
		if err != nil {
			return "", false
		}
		unicodeLabel, err := idna.Lookup.ToUnicode(ascii)
		if err != nil {
			return "", false
		}
		labels[i] = strings.ToLower(norm.NFC.String(unicodeLabel))
	}
	return strings.Join(labels, "."), true
}

func matchWebFetchPort(patternPort string, targetPort int) bool {
	if patternPort == "" || patternPort == "*" {
		return true
	}
	if targetPort < 0 {
		return false
	}
	if lo, hi, isRange := parseWebFetchPortRange(patternPort); isRange {
		return targetPort >= lo && targetPort <= hi
	}
	n, err := strconv.Atoi(patternPort)
	if err != nil || !validWebFetchPort(n) {
		return false
	}
	return n == targetPort
}

func parseWebFetchPortRange(s string) (lo, hi int, ok bool) {
	i := strings.IndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(s[:i])
	hi, err2 := strconv.Atoi(s[i+1:])
	if err1 != nil || err2 != nil || !validWebFetchPort(lo) || !validWebFetchPort(hi) || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

func validWebFetchPort(port int) bool {
	return port >= 1 && port <= 65535
}
