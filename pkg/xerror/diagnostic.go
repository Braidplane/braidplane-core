package xerror

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"net"
	"syscall"
)

// Classify returns the stable application code and kind, plus a bounded cause category.
// It never formats the error or exposes fields from dependency errors.
func Classify(err error) (code, kind, category string) {
	if err == nil {
		return "", "", ""
	}
	category = causeCategory(err)

	var appErr *Error
	if As(err, &appErr) && appErr != nil {
		return appErr.Code(), appErr.Kind().String(), category
	}
	if category != "" {
		return category, "", category
	}
	return "error", "", ""
}

func causeCategory(err error) string {
	var timeout interface{ Timeout() bool }
	var dns *net.DNSError
	var op *net.OpError
	var tlsVerification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	var database interface{ SQLState() string }

	switch {
	case Is(err, context.Canceled):
		return "request_canceled"
	case Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case As(err, &timeout) && timeout != nil && timeout.Timeout():
		return "timeout"
	case As(err, &dns):
		return "dns_error"
	case As(err, &tlsVerification), As(err, &unknownAuthority), As(err, &hostname), As(err, &certificateInvalid):
		return "tls_error"
	case As(err, &op), Is(err, syscall.ECONNREFUSED), Is(err, syscall.ECONNRESET),
		Is(err, syscall.ENETUNREACH), Is(err, syscall.EHOSTUNREACH), Is(err, syscall.EPIPE):
		return "network_error"
	case As(err, &database), Is(err, driver.ErrBadConn):
		return "database_error"
	default:
		return ""
	}
}
