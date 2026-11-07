package email

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"os"

	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var empty string

// ConfigTLS returns a *tls.Config configured with the provided TLSConfig and serverName.
// The certificate passed is added to the RootCA pool.
// Returns nil if no TLS configuration is needed.
func ConfigTLS(tlsCfg config.TLSConfig, serverName string) (*tls.Config, error) {
	// If no TLS config provided, return nil (let the mail library handle default TLS)
	if tlsCfg.CACert == empty && tlsCfg.CACertFile == empty && !tlsCfg.Insecure {
		//nolint:nilnil // nil tls config is intentional to let go-mail use defaults
		return nil, nil
	}

	tlscfg := &tls.Config{
		//nolint:gosec // InsecureSkipVerify is used to skip the TLS verification when the customer wishes to do so.
		InsecureSkipVerify: tlsCfg.Insecure,
		ServerName:         serverName,
	}

	// Early exit: Without any further configuration just return the skipVerify
	if tlsCfg.CACert == empty && tlsCfg.CACertFile == empty {
		return tlscfg, nil
	}

	// CACert has the priority over CACertPath
	if tlsCfg.CACert != empty {
		rootCAs, err := rootCAsFromCert([]byte(tlsCfg.CACert))
		if err == nil {
			tlscfg.RootCAs = rootCAs
			return tlscfg, nil
		}

		slog.Error("invalid CACert provided. Fallback to CACertFile", "error", err)
	}

	if tlsCfg.CACertFile == empty {
		return nil, xerror.Unexpected().Detail(
			"no valid CACert provided and no CACertFile to read from",
		)
	}

	// CACert bytes are either missing or invalid, so read the CACert from file.
	rootCAs, err := rootCAsFromCertFile(tlsCfg.CACertFile)
	if err != nil {
		return nil, xerror.Wrapf("load TLS root CAs: %w", err)
	}

	tlscfg.RootCAs = rootCAs
	return tlscfg, nil
}

func rootCAsFromCertFile(p string) (*x509.CertPool, error) {
	bb, err := os.ReadFile(p)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return rootCAsFromCert(bb)
}

func rootCAsFromCert(bb []byte) (*x509.CertPool, error) {
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Add the custom certificate authority
	if !rootCAs.AppendCertsFromPEM(bb) {
		return nil, xerror.Unexpected().Detail("failed to append provided CACert to root CAs")
	}

	return rootCAs, nil
}
