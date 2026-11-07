package datasourcessvc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ConnectionTestDiagnostic crosses the worker boundary without exposing provider error text.
func ConnectionTestDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	if detail := strings.TrimSpace(xerror.PublicDetail(err)); detail != "" {
		return detail
	}
	if xerror.Is(err, xerror.ExecutionUnavailable()) {
		return "The connection check could not run because the execution service is unavailable. Retry or contact an administrator."
	}
	if xerror.Is(err, context.Canceled) {
		return "Connection test was canceled. Retry the access check."
	}
	var networkError net.Error
	if xerror.Is(err, context.DeadlineExceeded) || (xerror.As(err, &networkError) && networkError.Timeout()) {
		return "Connection test timed out. Check the endpoint, network access, and whether the service is running."
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var tlsHeader tls.RecordHeaderError
	if xerror.As(err, &unknownAuthority) || xerror.As(err, &hostnameError) || xerror.As(err, &invalidCertificate) || xerror.As(err, &tlsHeader) {
		return "TLS connection failed. Check the TLS settings, server certificate, and hostname."
	}
	var dnsError *net.DNSError
	if xerror.As(err, &dnsError) {
		return "The data source hostname could not be resolved. Check the hostname and DNS configuration."
	}
	if networkError != nil {
		return "Could not reach the data source. Check the host, port, network access, and whether the service is running."
	}
	// Wrappers may carry a generic connection failure above the useful typed cause.
	causes := []error{err}
	unavailable := false
	for len(causes) > 0 {
		cause := causes[0]
		causes = causes[1:]
		if multi, ok := cause.(interface{ Unwrap() []error }); ok {
			causes = append(causes, multi.Unwrap()...)
		} else if child := xerror.Unwrap(cause); child != nil {
			causes = append(causes, child)
		}
		var typed *xerror.Error
		if !xerror.As(cause, &typed) || typed == nil {
			continue
		}
		switch typed.Kind() {
		case xerror.KindAlreadyExists, xerror.KindConnectionFailed, xerror.KindInternal:
			continue
		case xerror.KindUnauthorized:
			return "Authentication failed. Check the saved credentials or reconnect the provider, then recheck access."
		case xerror.KindForbidden:
			return "Access denied. Check the connection's permissions and access to the selected resource."
		case xerror.KindTimeout:
			return "Connection test timed out. Check the endpoint, network access, and whether the service is running."
		case xerror.KindNotFound:
			return "The selected resource was not found. Check that it exists and the connection can access it."
		case xerror.KindInvalidInput:
			return "Connection settings are invalid or incomplete. Check the configuration and credentials."
		case xerror.KindUnavailable:
			unavailable = true
		case xerror.KindUnsupported:
			return "This connection configuration is not supported. Check the connector settings."
		}
	}
	if unavailable {
		return "The data source service is unavailable. Retry later and check service availability."
	}
	return "Connection test failed. Check the connection settings and service availability. If the problem persists, contact an administrator."
}
