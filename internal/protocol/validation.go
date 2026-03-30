package protocol

import (
	"fmt"
	"net"
	"strings"
)

// ValidateDomain checks that domain is a syntactically valid DNS name per
// RFC 1035 (labels 1-63 chars, total <= 253 chars, alphanumeric + hyphens).
// IP addresses are also accepted as valid targets.
func ValidateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("empty domain")
	}

	// Accept valid IP addresses.
	if net.ParseIP(domain) != nil {
		return nil
	}

	if len(domain) > 253 {
		return fmt.Errorf("domain too long: %d chars (max 253)", len(domain))
	}

	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("invalid label length %d in domain %q", len(label), domain)
		}
		for i, c := range label {
			if !isAlphaNum(byte(c)) && c != '-' {
				return fmt.Errorf("invalid character %q in domain label %q", c, label)
			}
			if c == '-' && (i == 0 || i == len(label)-1) {
				return fmt.Errorf("label %q starts or ends with hyphen", label)
			}
		}
	}

	return nil
}

func isAlphaNum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ValidatePort checks that port is within [minPort, 65535].
func ValidatePort(port, minPort int) error {
	if port < minPort || port > 65535 {
		return fmt.Errorf("port %d out of range [%d, 65535]", port, minPort)
	}
	return nil
}

// ValidateTarget validates both the host and port of a target address.
func ValidateTarget(host string, port, minPort int) error {
	if err := ValidateDomain(host); err != nil {
		return fmt.Errorf("invalid target host: %w", err)
	}
	if err := ValidatePort(port, minPort); err != nil {
		return fmt.Errorf("invalid target port: %w", err)
	}
	return nil
}
