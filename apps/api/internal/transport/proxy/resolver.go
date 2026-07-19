// Package proxy resolves client addresses without trusting caller-controlled forwarding metadata by default.
package proxy

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

const (
	// MaximumForwardedAddresses bounds parsing work and the number of proxy hops accepted from one request.
	MaximumForwardedAddresses = 32
)

var (
	// ErrInvalidConfig rejects an empty or unsafe trusted-proxy allowlist.
	ErrInvalidConfig = errors.New("invalid trusted proxy configuration")
	// ErrInvalidPeer reports a missing or malformed socket peer that cannot safely identify a client.
	ErrInvalidPeer = errors.New("invalid request peer address")
)

// Anomaly is a closed, non-sensitive reason used by security metrics when forwarding metadata is ignored.
type Anomaly string

const (
	AnomalyUntrustedPeer      Anomaly = "untrusted_peer"
	AnomalyDuplicateHeader    Anomaly = "duplicate_header"
	AnomalyConflictingHeaders Anomaly = "conflicting_headers"
	AnomalyMalformedHeader    Anomaly = "malformed_header"
	AnomalyTooManyAddresses   Anomaly = "too_many_addresses"
	AnomalyAllTrusted         Anomaly = "all_addresses_trusted"
)

// String returns the bounded metric value and never includes an address or header fragment.
func (anomaly Anomaly) String() string { return string(anomaly) }

// Valid reports whether the reason belongs to the reviewed forwarding-header contract.
func (anomaly Anomaly) Valid() bool {
	switch anomaly {
	case AnomalyUntrustedPeer, AnomalyDuplicateHeader, AnomalyConflictingHeaders,
		AnomalyMalformedHeader, AnomalyTooManyAddresses, AnomalyAllTrusted:
		return true
	default:
		return false
	}
}

// Observer receives only bounded reasons; raw peers and forwarding values remain owned by the resolver.
type Observer interface {
	ObserveProxyAnomaly(Anomaly)
}

// Resolver owns an immutable trusted CIDR set and applies right-to-left proxy peeling.
type Resolver struct {
	trusted  []netip.Prefix
	observer Observer
}

// NewResolver revalidates configured CIDRs so direct construction cannot accidentally trust all addresses.
func NewResolver(trusted []netip.Prefix, observer Observer) (*Resolver, error) {
	if len(trusted) == 0 {
		return nil, ErrInvalidConfig
	}
	cloned := make([]netip.Prefix, len(trusted))
	for index, prefix := range trusted {
		prefix = prefix.Masked()
		if !prefix.IsValid() || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, ErrInvalidConfig
		}
		for earlier := 0; earlier < index; earlier++ {
			if cloned[earlier].Addr().BitLen() == prefix.Addr().BitLen() &&
				(cloned[earlier].Contains(prefix.Addr()) || prefix.Contains(cloned[earlier].Addr())) {
				return nil, ErrInvalidConfig
			}
		}
		cloned[index] = prefix
	}
	return &Resolver{trusted: cloned, observer: observer}, nil
}

// Resolve returns the first untrusted address to the left of a continuous trusted proxy suffix.
// Invalid or ambiguous forwarding metadata is ignored in full and falls back to the socket peer.
func (resolver *Resolver) Resolve(request *http.Request) (netip.Addr, error) {
	if resolver == nil || request == nil {
		return netip.Addr{}, ErrInvalidPeer
	}
	peer, err := parsePeer(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, ErrInvalidPeer
	}
	forwardedValues := request.Header.Values("Forwarded")
	xffValues := request.Header.Values("X-Forwarded-For")
	hasForwardingHeaders := len(forwardedValues) > 0 || len(xffValues) > 0
	if !hasForwardingHeaders {
		return peer, nil
	}
	if !resolver.isTrusted(peer) {
		resolver.observe(AnomalyUntrustedPeer)
		return peer, nil
	}
	if len(forwardedValues) > 1 || len(xffValues) > 1 {
		resolver.observe(AnomalyDuplicateHeader)
		return peer, nil
	}

	var chain []netip.Addr
	if len(forwardedValues) == 1 {
		parsed, anomaly := parseForwarded(forwardedValues[0])
		if anomaly.Valid() {
			resolver.observe(anomaly)
			return peer, nil
		}
		chain = parsed
	}
	if len(xffValues) == 1 {
		parsed, anomaly := parseXForwardedFor(xffValues[0])
		if anomaly.Valid() {
			resolver.observe(anomaly)
			return peer, nil
		}
		if chain != nil && !equalAddresses(chain, parsed) {
			resolver.observe(AnomalyConflictingHeaders)
			return peer, nil
		}
		chain = parsed
	}
	if len(chain) == 0 {
		resolver.observe(AnomalyMalformedHeader)
		return peer, nil
	}
	for index := len(chain) - 1; index >= 0; index-- {
		if !resolver.isTrusted(chain[index]) {
			return chain[index], nil
		}
	}
	resolver.observe(AnomalyAllTrusted)
	return peer, nil
}

func (resolver *Resolver) isTrusted(address netip.Addr) bool {
	for _, prefix := range resolver.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (resolver *Resolver) observe(anomaly Anomaly) {
	if resolver.observer != nil {
		resolver.observer.ObserveProxyAnomaly(anomaly)
	}
}

func parsePeer(value string) (netip.Addr, error) {
	addressPort, err := netip.ParseAddrPort(strings.TrimSpace(value))
	if err == nil {
		return validateAddress(addressPort.Addr())
	}
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, ErrInvalidPeer
	}
	return validateAddress(address)
}

func parseForwarded(value string) ([]netip.Addr, Anomaly) {
	elements, ok := splitHeaderValue(value, ',')
	if !ok || len(elements) == 0 {
		return nil, AnomalyMalformedHeader
	}
	if len(elements) > MaximumForwardedAddresses {
		return nil, AnomalyTooManyAddresses
	}
	addresses := make([]netip.Addr, 0, len(elements))
	for _, element := range elements {
		parameters, valid := splitHeaderValue(element, ';')
		if !valid || len(parameters) == 0 {
			return nil, AnomalyMalformedHeader
		}
		var node string
		for _, parameter := range parameters {
			name, parameterValue, found := strings.Cut(strings.TrimSpace(parameter), "=")
			name = strings.TrimSpace(name)
			parameterValue = strings.TrimSpace(parameterValue)
			if !found || !validToken(name) || parameterValue == "" {
				return nil, AnomalyMalformedHeader
			}
			if strings.EqualFold(name, "for") {
				if node != "" {
					return nil, AnomalyMalformedHeader
				}
				node = parameterValue
			}
		}
		if node == "" {
			return nil, AnomalyMalformedHeader
		}
		address, err := parseForwardedAddress(node)
		if err != nil {
			return nil, AnomalyMalformedHeader
		}
		addresses = append(addresses, address)
	}
	return addresses, ""
}

func parseXForwardedFor(value string) ([]netip.Addr, Anomaly) {
	parts, ok := splitHeaderValue(value, ',')
	if !ok || len(parts) == 0 {
		return nil, AnomalyMalformedHeader
	}
	if len(parts) > MaximumForwardedAddresses {
		return nil, AnomalyTooManyAddresses
	}
	addresses := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		address, err := parseForwardedAddress(strings.TrimSpace(part))
		if err != nil {
			return nil, AnomalyMalformedHeader
		}
		addresses = append(addresses, address)
	}
	return addresses, ""
}

func parseForwardedAddress(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
		if strings.ContainsAny(value, "\\\"") {
			return netip.Addr{}, ErrInvalidPeer
		}
	}
	if value == "" || strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
		return netip.Addr{}, ErrInvalidPeer
	}
	if addressPort, err := netip.ParseAddrPort(value); err == nil {
		return validateAddress(addressPort.Addr())
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = value[1 : len(value)-1]
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, ErrInvalidPeer
	}
	return validateAddress(address)
}

func validateAddress(address netip.Addr) (netip.Addr, error) {
	if !address.IsValid() || address.Is4In6() || address.IsUnspecified() || address.IsMulticast() || address.Zone() != "" {
		return netip.Addr{}, ErrInvalidPeer
	}
	return address, nil
}

func splitHeaderValue(value string, delimiter byte) ([]string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	var result []string
	start := 0
	quoted := false
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '"':
			quoted = !quoted
		case '\\':
			if !quoted || index+1 >= len(value) {
				return nil, false
			}
			index++
		default:
			if value[index] == delimiter && !quoted {
				part := strings.TrimSpace(value[start:index])
				if part == "" {
					return nil, false
				}
				result = append(result, part)
				start = index + 1
			}
		}
	}
	if quoted {
		return nil, false
	}
	last := strings.TrimSpace(value[start:])
	if last == "" {
		return nil, false
	}
	return append(result, last), true
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			continue
		}
		return false
	}
	return true
}

func equalAddresses(left, right []netip.Addr) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
