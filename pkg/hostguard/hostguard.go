package hostguard

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var blockedPublicHostPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// IsBlockedHostnameLiteral reports whether a hostname string is a blocked IP literal.
// Non-IP hostnames return false because they must be resolved separately.
func IsBlockedHostnameLiteral(hostname string) bool {
	if strings.Contains(hostname, ":") && !strings.ContainsAny(hostname, "/") {
		hostname = strings.Trim(hostname, "[]")
	}

	ip := net.ParseIP(hostname)
	if ip == nil {
		return false
	}

	return IsBlockedIP(ip)
}

// PublicDialer resolves and dials only public IP addresses for host:port targets.
type PublicDialer struct {
	Dialer *net.Dialer
}

// DestinationPolicy defines whether a connector may reach private addresses while always
// resolving the requested hostname and dialing a returned literal IP address.
type DestinationPolicy struct {
	AllowPrivateHosts bool
}

// NewDestinationPolicy creates the shared destination policy used by connector transports.
func NewDestinationPolicy(allowPrivateHosts bool) DestinationPolicy {
	return DestinationPolicy{AllowPrivateHosts: allowPrivateHosts}
}

// DialContext resolves the requested destination once and dials a vetted literal address.
// Callers retain the original hostname in protocol configuration so TLS SNI and Host headers
// continue to identify the intended server.
func (p DestinationPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return p.DialContextWithDialer(ctx, network, address, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

// DialContextWithDialer resolves and dials a destination through injectable lookup and dial
// functions so protocol adapters can prove that the socket never receives the hostname.
func (p DestinationPolicy) DialContextWithDialer(
	ctx context.Context,
	network string,
	address string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("split hostguard dial address")
	}

	addrs, err := p.ResolveWithLookup(ctx, host, lookup)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, addr := range addrs {
		if !ipMatchesNetwork(addr.IP, network) {
			continue
		}
		dialHost := addr.IP.String()
		if addr.Zone != "" {
			dialHost += "%" + addr.Zone
		}
		conn, err := dial(ctx, network, net.JoinHostPort(dialHost, port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, xerror.Unexpected().With(lastErr)
	}
	return nil, xerror.Unexpected().Detail("hostguard dial found no address for network")
}

// Resolve resolves a hostname under this policy and returns the literal addresses that may be used.
func (p DestinationPolicy) Resolve(ctx context.Context, hostname string) ([]net.IPAddr, error) {
	return p.ResolveWithLookup(ctx, hostname, net.DefaultResolver.LookupIPAddr)
}

// ResolveWithLookup applies the policy to the supplied DNS lookup result without performing a
// second resolution, which prevents callers from validating one DNS answer and dialing another.
func (p DestinationPolicy) ResolveWithLookup(
	ctx context.Context,
	hostname string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
) ([]net.IPAddr, error) {
	hostname = strings.Trim(strings.TrimSpace(hostname), "[]")
	if hostname == "" {
		return nil, xerror.Unexpected().Detail("hostname is empty")
	}

	if ip := net.ParseIP(hostname); ip != nil {
		if !p.AllowPrivateHosts && IsBlockedIP(ip) {
			return nil, xerror.Unexpected().Detail("host resolves to a non-public ip address")
		}
		return []net.IPAddr{{IP: ip}}, nil
	}

	addrs, err := lookup(ctx, hostname)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if len(addrs) == 0 {
		return nil, xerror.Unexpected().Detail("hostname resolved to no IP addresses")
	}

	resolved := make([]net.IPAddr, 0, len(addrs))
	for _, addr := range addrs {
		if !p.AllowPrivateHosts && IsBlockedIP(addr.IP) {
			return nil, xerror.Unexpected().Detail("host resolves to a non-public ip address")
		}
		resolved = append(resolved, net.IPAddr{IP: addr.IP, Zone: addr.Zone})
	}

	return resolved, nil
}

// DialContext resolves the target host, rejects private/special-use addresses, and dials
// one of the vetted IPs so the caller cannot re-resolve a different address later.
func (d PublicDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := d.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return NewDestinationPolicy(false).DialContextWithDialer(
		ctx,
		network,
		address,
		net.DefaultResolver.LookupIPAddr,
		dialer.DialContext,
	)
}

// DialPublicHost resolves and dials a host:port address only when all resolved
// addresses are public.
func DialPublicHost(ctx context.Context, network, address string) (net.Conn, error) {
	return PublicDialer{}.DialContext(ctx, network, address)
}

// DialPublicHostWithDialer is the testable implementation behind PublicDialer.
func DialPublicHostWithDialer(
	ctx context.Context,
	network string,
	address string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	return NewDestinationPolicy(false).DialContextWithDialer(ctx, network, address, lookup, dial)
}

// ipMatchesNetwork reports whether an IP is compatible with a requested TCP network family.
func ipMatchesNetwork(ip net.IP, network string) bool {
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}

// IsBlockedIP reports whether an IP falls into blocked non-public or special-use ranges.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}

	addr = addr.Unmap()
	for _, prefix := range blockedPublicHostPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

// ValidateResolvedPublicHost resolves a hostname and rejects blocked/private targets.
func ValidateResolvedPublicHost(ctx context.Context, hostname string) error {
	_, err := ResolvePublicHost(ctx, hostname)
	return err
}

// ResolvePublicHost resolves a hostname once and returns vetted public IP addresses.
func ResolvePublicHost(ctx context.Context, hostname string) ([]net.IPAddr, error) {
	return ResolvePublicHostWithLookup(ctx, hostname, net.DefaultResolver.LookupIPAddr)
}

// ResolvePublicHostWithLookup resolves a hostname once with the provided lookup function and
// returns vetted public IP addresses.
func ResolvePublicHostWithLookup(
	ctx context.Context,
	hostname string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
) ([]net.IPAddr, error) {
	return NewDestinationPolicy(false).ResolveWithLookup(ctx, hostname, lookup)
}
