package proxy

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestResolverPeelsOnlyContinuousTrustedProxies(t *testing.T) {
	recorder := &anomalyRecorder{}
	resolver := mustResolver(t, recorder, "10.0.0.0/8")
	request := requestWithPeer("10.0.0.20:443")
	request.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.5, 10.0.0.10")

	client, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("198.51.100.5"); client != want {
		t.Fatalf("client IP = %s, want %s", client, want)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("unexpected anomalies = %v", recorder.events)
	}
}

func TestResolverAcceptsEquivalentForwardedAndXFF(t *testing.T) {
	resolver := mustResolver(t, nil, "10.0.0.0/8")
	request := requestWithPeer("10.0.0.20:443")
	request.Header.Set("Forwarded", `for=198.51.100.8;proto=https, for="10.0.0.10:8443"`)
	request.Header.Set("X-Forwarded-For", "198.51.100.8, 10.0.0.10")

	client, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("198.51.100.8"); client != want {
		t.Fatalf("client IP = %s, want %s", client, want)
	}
}

func TestResolverIgnoresForwardingHeadersFromUntrustedPeer(t *testing.T) {
	recorder := &anomalyRecorder{}
	resolver := mustResolver(t, recorder, "10.0.0.0/8")
	request := requestWithPeer("198.51.100.20:443")
	request.Header.Set("X-Forwarded-For", "203.0.113.10")

	client, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("198.51.100.20"); client != want {
		t.Fatalf("client IP = %s, want peer %s", client, want)
	}
	recorder.assertOnly(t, AnomalyUntrustedPeer)
}

func TestResolverRejectsAmbiguousOrMalformedForwardingHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    Anomaly
	}{
		{
			name: "duplicate XFF field lines",
			headers: http.Header{
				"X-Forwarded-For": []string{"198.51.100.1", "198.51.100.2"},
			},
			want: AnomalyDuplicateHeader,
		},
		{
			name: "conflicting standardized and legacy chains",
			headers: http.Header{
				"Forwarded":       []string{"for=198.51.100.1"},
				"X-Forwarded-For": []string{"198.51.100.2"},
			},
			want: AnomalyConflictingHeaders,
		},
		{
			name: "malformed forwarded node",
			headers: http.Header{
				"Forwarded": []string{"for=_hidden"},
			},
			want: AnomalyMalformedHeader,
		},
		{
			name: "too many addresses",
			headers: http.Header{
				"X-Forwarded-For": []string{strings.Repeat("198.51.100.1,", MaximumForwardedAddresses) + "198.51.100.2"},
			},
			want: AnomalyTooManyAddresses,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &anomalyRecorder{}
			resolver := mustResolver(t, recorder, "10.0.0.0/8")
			request := requestWithPeer("10.0.0.20:443")
			request.Header = test.headers
			client, err := resolver.Resolve(request)
			if err != nil {
				t.Fatal(err)
			}
			if want := netip.MustParseAddr("10.0.0.20"); client != want {
				t.Fatalf("ambiguous client IP = %s, want peer %s", client, want)
			}
			recorder.assertOnly(t, test.want)
		})
	}
}

func TestResolverRejectsAllTrustedChain(t *testing.T) {
	recorder := &anomalyRecorder{}
	resolver := mustResolver(t, recorder, "10.0.0.0/8")
	request := requestWithPeer("10.0.0.20:443")
	request.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.10")

	client, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("10.0.0.20"); client != want {
		t.Fatalf("all-trusted client IP = %s, want peer %s", client, want)
	}
	recorder.assertOnly(t, AnomalyAllTrusted)
}

func TestResolverUsesPeerWithoutHeaders(t *testing.T) {
	resolver := mustResolver(t, nil, "10.0.0.0/8")
	request := requestWithPeer("[2001:db8::10]:443")
	client, err := resolver.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("2001:db8::10"); client != want {
		t.Fatalf("direct client IP = %s, want %s", client, want)
	}
}

func mustResolver(t testing.TB, observer Observer, prefixes ...string) *Resolver {
	t.Helper()
	trusted := make([]netip.Prefix, len(prefixes))
	for index, prefix := range prefixes {
		trusted[index] = netip.MustParsePrefix(prefix)
	}
	resolver, err := NewResolver(trusted, observer)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func requestWithPeer(peer string) *http.Request {
	return &http.Request{RemoteAddr: peer, Header: make(http.Header)}
}

type anomalyRecorder struct {
	events []Anomaly
}

func (recorder *anomalyRecorder) ObserveProxyAnomaly(anomaly Anomaly) {
	recorder.events = append(recorder.events, anomaly)
}

func (recorder *anomalyRecorder) assertOnly(t testing.TB, want Anomaly) {
	t.Helper()
	if len(recorder.events) != 1 || recorder.events[0] != want {
		t.Fatalf("proxy anomalies = %v, want [%s]", recorder.events, want)
	}
}
