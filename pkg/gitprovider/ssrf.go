package gitprovider

import (
	"context"
	"net"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type (
	lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)
	dialContextFunc  func(context.Context, string, string) (net.Conn, error)
)

// SSRFProtectedDialContext returns a dialer that connects only to vetted public IPs.
func SSRFProtectedDialContext() func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return SSRFProtectedDialContextWithLookup(net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

// SSRFProtectedDialContextWithLookup builds a testable dialer from explicit lookup and dial functions.
func SSRFProtectedDialContextWithLookup(
	lookup lookupIPAddrFunc,
	dial dialContextFunc,
) func(context.Context, string, string) (net.Conn, error) {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}

		publicAddrs, err := hostguard.ResolvePublicHostWithLookup(ctx, host, lookup)
		if err != nil {
			return nil, xerror.Wrapf("ssrf protection: %w", err)
		}

		var firstErr error
		for _, addr := range publicAddrs {
			conn, dialErr := dial(ctx, network, net.JoinHostPort(addr.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = dialErr
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}

		return nil, xerror.Unexpected().Detail("hostname resolved to no IP addresses")
	}
}
