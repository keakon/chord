package mcp

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	httpInvalidChallengeSummary = "mcp http: invalid authentication challenge"
	httpInvalidProtocolSummary  = "mcp http: invalid protocol response"
)

// SendEvidence describes what the transport can prove about a request.
type SendEvidence string

const (
	SendNotSent          SendEvidence = "not_sent"
	SendResponseReceived SendEvidence = "response_received"
	SendOutcomeUnknown   SendEvidence = "outcome_unknown"
)

// BearerChallenge contains only parameters needed by OAuth. It is an internal
// protocol value, not a diagnostic DTO; URLs and scopes must not be logged.
type BearerChallenge struct {
	ResourceMetadata string
	Scope            string
	Error            string
}

// HTTPError never includes an HTTP response body or raw authentication header.
type HTTPError struct {
	StatusCode       int
	BearerChallenges []BearerChallenge
	SafeSummary      string
	SendEvidence     SendEvidence
}

func (e *HTTPError) Error() string { return e.SafeSummary }

// transportFailure retains cancellation/type evidence without exposing a URL or
// another potentially sensitive value from the underlying network error.
type transportFailure struct {
	detail *HTTPError
	cause  error
}

func (e *transportFailure) Error() string   { return e.detail.Error() }
func (e *transportFailure) Unwrap() []error { return []error{e.detail, e.cause} }

func newTransportFailure(summary string, evidence SendEvidence, cause error) error {
	return &transportFailure{detail: &HTTPError{SafeSummary: summary, SendEvidence: evidence}, cause: cause}
}

func responseHTTPError(resp *http.Response) *HTTPError {
	challenges, err := ParseBearerChallenges(resp.Header.Values("WWW-Authenticate"))
	summary := fmt.Sprintf("mcp http: server returned %d", resp.StatusCode)
	if err != nil {
		summary = httpInvalidChallengeSummary
	}
	return &HTTPError{StatusCode: resp.StatusCode, BearerChallenges: challenges, SafeSummary: summary, SendEvidence: SendResponseReceived}
}

const maxAuthenticateHeaderBytes = 16 << 10

// ParseBearerChallenges validates the complete challenge syntax before exposing
// Bearer parameters. Non-Bearer parameters can never become OAuth hints.
func ParseBearerChallenges(headers []string) ([]BearerChallenge, error) {
	total := 0
	for _, h := range headers {
		total += len(h)
		if total > maxAuthenticateHeaderBytes {
			return nil, fmt.Errorf("authentication challenge exceeds size limit")
		}
	}
	var result []BearerChallenge
	metadata := ""
	for _, h := range headers {
		p := challengeParser{s: h}
		for {
			p.spaces()
			if p.pos == len(p.s) {
				break
			}
			scheme := p.token()
			if scheme == "" {
				return nil, invalidChallenge()
			}
			bearer := strings.EqualFold(scheme, "Bearer")
			c := BearerChallenge{}
			seen := map[string]bool{}
			if p.pos < len(p.s) && p.s[p.pos] != ',' {
				if p.s[p.pos] != ' ' && p.s[p.pos] != '\t' {
					return nil, invalidChallenge()
				}
				p.spaces()
				// token68 is an alternative to an auth-param list (e.g. Negotiate).
				start := p.pos
				for p.pos < len(p.s) && isToken68(p.s[p.pos]) {
					p.pos++
				}
				for p.pos < len(p.s) && p.s[p.pos] == '=' {
					p.pos++
				}
				end := p.pos
				p.spaces()
				token68 := end > start && (p.pos == len(p.s) || p.s[p.pos] == ',')
				if !token68 {
					p.pos = start
					for {
						key := strings.ToLower(p.token())
						if key == "" {
							return nil, invalidChallenge()
						}
						p.spaces()
						if !p.take('=') {
							return nil, invalidChallenge()
						}
						p.spaces()
						value, ok := p.value()
						if !ok {
							return nil, invalidChallenge()
						}
						if bearer {
							switch key {
							case "resource_metadata", "scope", "error", "error_description", "error_uri", "realm":
								if seen[key] {
									return nil, invalidChallenge()
								}
								seen[key] = true
							}
							switch key {
							case "resource_metadata":
								c.ResourceMetadata = value
							case "scope":
								c.Scope = value
							case "error":
								c.Error = value
							}
						}
						p.spaces()
						if p.pos == len(p.s) {
							break
						}
						if !p.take(',') {
							return nil, invalidChallenge()
						}
						p.spaces()
						next := p.pos
						if p.token() == "" {
							return nil, invalidChallenge()
						}
						p.spaces()
						param := p.pos < len(p.s) && p.s[p.pos] == '='
						p.pos = next
						if !param {
							break
						}
					}
				} else if p.pos < len(p.s) {
					p.pos++
					p.spaces()
					if p.pos == len(p.s) {
						return nil, invalidChallenge()
					}
				}
			} else if p.take(',') {
				p.spaces()
				if p.pos == len(p.s) {
					return nil, invalidChallenge()
				}
			}
			if bearer {
				if c.ResourceMetadata != "" {
					if metadata != "" && metadata != c.ResourceMetadata {
						return nil, fmt.Errorf("conflicting authentication resource metadata")
					}
					metadata = c.ResourceMetadata
				}
				result = append(result, c)
			}
		}
	}
	return result, nil
}

func invalidChallenge() error { return fmt.Errorf("malformed authentication challenge") }

type challengeParser struct {
	s   string
	pos int
}

func (p *challengeParser) spaces() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}
func (p *challengeParser) take(c byte) bool {
	if p.pos < len(p.s) && p.s[p.pos] == c {
		p.pos++
		return true
	}
	return false
}
func isToken(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))
}
func isToken68(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~+/", rune(c))
}
func (p *challengeParser) token() string {
	start := p.pos
	for p.pos < len(p.s) && isToken(p.s[p.pos]) {
		p.pos++
	}
	return p.s[start:p.pos]
}
func (p *challengeParser) value() (string, bool) {
	if !p.take('"') {
		s := p.token()
		return s, s != ""
	}
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		p.pos++
		if c == '"' {
			return b.String(), true
		}
		if c == '\\' {
			if p.pos == len(p.s) {
				return "", false
			}
			c = p.s[p.pos]
			p.pos++
		}
		if c < 0x20 || c == 0x7f {
			return "", false
		}
		b.WriteByte(c)
	}
	return "", false
}
