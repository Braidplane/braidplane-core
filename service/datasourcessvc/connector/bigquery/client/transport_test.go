package client

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"

	"github.com/Braidplane/braidplane-core/pkg/googleclient"
)

func TestProviderTransportPinsVettedDNSAnswer(t *testing.T) {
	// This test replaces the process resolver, so it must not run in parallel.
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })
	var lookups atomic.Int32
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			var length uint16
			if binary.Read(server, binary.BigEndian, &length) != nil {
				return
			}
			query := make([]byte, length)
			if _, err := io.ReadFull(server, query); err != nil {
				return
			}
			var message dnsmessage.Message
			if message.Unpack(query) != nil {
				return
			}
			message.Response, message.RecursionAvailable = true, true
			for _, question := range message.Questions {
				if question.Type != dnsmessage.TypeA {
					continue
				}
				ip := [4]byte{93, 184, 216, 34}
				if lookups.Add(1) > 1 {
					ip = [4]byte{127, 0, 0, 1}
				}
				message.Answers = append(message.Answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1},
					Body:   &dnsmessage.AResource{A: ip},
				})
			}
			response, err := message.Pack()
			if err != nil {
				return
			}
			responseLength := len(response)
			if responseLength > 65535 {
				return
			}
			if binary.Write(server, binary.BigEndian, uint16(responseLength)) != nil {
				return
			}
			_, _ = server.Write(response)
		}()
		return client, nil
	}}
	transport, err := providerTransport(false)
	require.NoError(t, err)
	// UDP connects without sending traffic; its remote address proves the actual
	// socket destination while the same dial policy handles production TCP.
	conn, err := transport.(*http.Transport).DialContext(transportsecurity.WithPlaintext(t.Context()), "udp4", "rebind.example.test:443")
	if err != nil {
		// Sandboxes may reject socket creation; the failed syscall still records its destination.
		var dialErr *net.OpError
		require.ErrorAs(t, err, &dialErr)
		require.NotNil(t, dialErr.Addr)
		require.Equal(t, "93.184.216.34:443", dialErr.Addr.String())
	} else {
		t.Cleanup(func() { _ = conn.Close() })
		require.Equal(t, "93.184.216.34:443", conn.RemoteAddr().String())
	}
	require.EqualValues(t, 1, lookups.Load(), "a second lookup would return loopback")
}

func TestProviderTransportPrivatePolicyAndTokenExchange(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, tokenHandler(t, "fixture-token"))
	for _, allowPrivate := range []bool{false, true} {
		transport, err := providerTransport(allowPrivate)
		require.NoError(t, err)
		httpClient := googleclient.NewHTTPClient(defaultTimeout, transport)
		t.Cleanup(httpClient.CloseIdleConnections)
		c := newTestClient(t, server.URL, server.URL+"/token")
		c.client = httpClient
		_, _, err = c.exchangeToken(transportsecurity.WithPlaintext(t.Context()))
		if allowPrivate {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestTokenExchangeNeverFollowsRedirects(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var destinations atomic.Int32
			destination := newTestServer(t, func(http.ResponseWriter, *http.Request) { destinations.Add(1) })
			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(status)
			})
			c := newTestClient(t, server.URL, server.URL+"/token")
			_, _, err := c.exchangeToken(transportsecurity.WithPlaintext(t.Context()))
			require.Error(t, err)
			require.Zero(t, destinations.Load())
		})
	}
}
