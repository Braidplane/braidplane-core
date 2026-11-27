package mcpauthsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const maxClientMetadataBytes = 5 * 1024

var errClientMetadataUnavailable = xerror.New(xerror.KindUnavailable, "MCP_OAUTH_CLIENT_METADATA_UNAVAILABLE", "OAuth client metadata is unavailable")

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

// IANA special-purpose ranges inside global unicast that hostguard does not already block.
var metadataExcludedPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
}

func newMetadataHTTPClient() *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DisableCompression:     true,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 16 * 1024,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return dialMetadataHost(ctx, network, address, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
			},
		},
	}
}

func dialMetadataHost(
	ctx context.Context, network, address string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalidClient
	}
	if ip := net.ParseIP(host); ip != nil && !metadataAddressAllowed(ip) {
		return nil, ErrInvalidClient
	}
	return hostguard.DialPublicHostWithDialer(ctx, network, address, func(ctx context.Context, name string) ([]net.IPAddr, error) {
		resolved, err := lookup(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, addr := range resolved {
			if addr.Zone != "" || !metadataAddressAllowed(addr.IP) {
				return nil, ErrInvalidClient
			}
		}
		return resolved, nil
	}, dial)
}

func metadataAddressAllowed(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || !addr.IsGlobalUnicast() || hostguard.IsBlockedIP(ip) {
		return false
	}
	addr = addr.Unmap()
	if addr.Is6() && !ipv6GlobalUnicast.Contains(addr) {
		return false
	}
	for _, prefix := range metadataExcludedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func (d *Deployment) resolveClient(ctx context.Context, id string) (Client, error) {
	if client, ok := d.clients[id]; ok {
		client.RedirectURIs = slices.Clone(client.RedirectURIs)
		return client, nil
	}
	u, ok := webURL(id)
	if !ok || u.Scheme != "https" || u.Path == "" || u.Path == "/" ||
		u.ForceQuery || u.RawQuery != "" || hasDotSegment(u.Path) || isLoopbackHost(u.Hostname()) {
		return Client{}, ErrInvalidClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return Client{}, ErrInvalidClient
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.metadataHTTP.Do(req)
	if err != nil {
		if xerror.Is(err, ErrInvalidClient) {
			return Client{}, ErrInvalidClient
		}
		return Client{}, errClientMetadataUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests {
		return Client{}, errClientMetadataUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	isJSON := mediaType == "application/json" || (strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))
	if resp.StatusCode != http.StatusOK || err != nil || !isJSON {
		return Client{}, ErrInvalidClient
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxClientMetadataBytes+1))
	if err != nil {
		return Client{}, errClientMetadataUnavailable
	}
	if len(data) > maxClientMetadataBytes {
		return Client{}, ErrInvalidClient
	}
	return parseClientMetadata(id, data)
}

func parseClientMetadata(id string, data []byte) (Client, error) {
	var client Client
	var authMethod string
	var authMethods, grantTypes, responseTypes []string
	if err := decodeClientMetadata(data, map[string]any{
		"client_id": &client.ID, "client_name": &client.Name, "redirect_uris": &client.RedirectURIs,
		"token_endpoint_auth_method": &authMethod, "token_endpoint_auth_methods_supported": &authMethods,
		"grant_types": &grantTypes, "response_types": &responseTypes,
	}); err != nil || client.ID != id || !validClient(client) {
		return Client{}, ErrInvalidClient
	}
	for _, method := range authMethods {
		if strings.HasPrefix(method, "client_secret_") {
			return Client{}, ErrInvalidClient
		}
	}
	if (authMethod != "" && authMethod != "none") ||
		(authMethods != nil && !slices.Contains(authMethods, "none")) ||
		(grantTypes != nil && !slices.Contains(grantTypes, "authorization_code")) ||
		(responseTypes != nil && !slices.Contains(responseTypes, "code")) {
		return Client{}, ErrInvalidClient
	}
	return client, nil
}

func decodeClientMetadata(data []byte, fields map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if first, err := decoder.Token(); err != nil || first != json.Delim('{') {
		return ErrInvalidClient
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || key == "client_secret" || key == "client_secret_expires_at" {
			return ErrInvalidClient
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return ErrInvalidClient
		}
		if field, ok := fields[key]; ok && (bytes.Equal(value, []byte("null")) || json.Unmarshal(value, field) != nil) {
			return ErrInvalidClient
		}
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalidClient
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidClient
	}
	return nil
}
