package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/i18n"
	"github.com/Braidplane/braidplane-core/service/email"
	"github.com/stretchr/testify/require"
)

func TestCustomerSMTPTrustAndRecoveryDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local SMTP sockets")
	}
	for _, tc := range []struct {
		name, certIP               string
		trusted, startTLS, success bool
	}{
		{name: "customer CA", certIP: "127.0.0.1", trusted: true, startTLS: true, success: true},
		{name: "untrusted CA", certIP: "127.0.0.1", startTLS: true},
		{name: "wrong hostname", certIP: "192.0.2.8", trusted: true, startTLS: true},
		{name: "no STARTTLS", certIP: "127.0.0.1", trusted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, ca := customerSMTPCertificate(t, tc.certIP)
			host, messages := customerSMTPFixture(t, cert, tc.startTLS)
			cfg := config.SMTP{Host: host, NoReplyAddress: "recovery@customer.example.test"}
			if tc.trusted {
				path := filepath.Join(t.TempDir(), "customer-ca.pem")
				require.NoError(t, os.WriteFile(path, ca, 0o600))
				cfg.CACertFile = path
			}
			bundle, err := i18n.NewBundle("en")
			require.NoError(t, err)
			sender, err := email.NewService(cfg, bundle)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err = sender.PasswordReset(ctx, &model.User{Email: "owner@customer.example.test", Locale: "en"}, "https://app.customer.example.test/reset-password?token=synthetic-reset-token")
			if !tc.success {
				require.Error(t, err)
				require.Empty(t, messages)
				return
			}
			require.NoError(t, err)
			select {
			case message := <-messages:
				require.Contains(t, message, "owner@customer.example.test")
				require.Contains(t, message, "synthetic-reset-token")
			case <-ctx.Done():
				t.Fatal("customer SMTP did not receive recovery email")
			}
		})
	}
}

func customerSMTPCertificate(t *testing.T, ip string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Synthetic Customer SMTP"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP(ip)}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	cert, err := tls.X509KeyPair(ca, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	return cert, ca
}

func customerSMTPFixture(t *testing.T, cert tls.Certificate, offerTLS bool) (string, chan string) {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	messages := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		wire := textproto.NewConn(conn)
		if wire.PrintfLine("220 customer.example.test ESMTP") != nil {
			return
		}
		encrypted := false
		for {
			line, err := wire.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				if offerTLS && !encrypted {
					err = wire.PrintfLine("250-customer.example.test\r\n250 STARTTLS")
				} else {
					err = wire.PrintfLine("250 customer.example.test")
				}
			case line == "STARTTLS":
				if !offerTLS {
					return
				}
				if wire.PrintfLine("220 Ready for TLS") != nil {
					return
				}
				secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if secured.HandshakeContext(t.Context()) != nil {
					return
				}
				wire = textproto.NewConn(secured)
				encrypted = true
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"), line == "RSET", line == "NOOP":
				err = wire.PrintfLine("250 OK")
			case line == "DATA":
				if !encrypted {
					return
				}
				if wire.PrintfLine("354 End with dot") != nil {
					return
				}
				data, readErr := wire.ReadDotBytes()
				if readErr != nil {
					return
				}
				messages <- string(data)
				err = wire.PrintfLine("250 Accepted")
			case line == "QUIT":
				_ = wire.PrintfLine("221 Bye")
				return
			default:
				err = wire.PrintfLine("500 Unsupported")
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String(), messages
}
