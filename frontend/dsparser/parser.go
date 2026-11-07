// Package dsparser provides connection URI parsers for each supported datasource type.
// Each parser converts a canonical connection URI into structured fields that match
// the API schema's config and credentials for that datasource.
package dsparser

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ErrUnsupportedScheme is returned when the URI scheme does not match the expected datasource type.
var ErrUnsupportedScheme = xerror.UnsupportedURIScheme()

// ParsedFields holds the structured fields extracted from a connection URI.
// Only fields relevant to the parsed datasource type will be populated.
type ParsedFields struct {
	// Common connection fields
	Host     string
	Port     int
	Database string

	// Credentials
	Username string
	Password string

	// PostgreSQL-specific
	SSLMode string

	// MongoDB-specific
	AuthDatabase string

	// HTTP-specific
	BaseURL     string
	HealthPath  string
	BearerToken string
}

// ParsePostgres parses a PostgreSQL connection URI.
// Supported schemes: postgres, postgresql.
// Example: postgres://user:pass@host:5432/dbname?sslmode=disable
func ParsePostgres(uri string) (*ParsedFields, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	switch u.Scheme {
	case "postgres", "postgresql":
	default:
		return nil, ErrUnsupportedScheme.Detailf("%q (expected postgres or postgresql)", u.Scheme)
	}

	host, portStr, err := splitHostPort(u.Host, "5432")
	if err != nil {
		return nil, xerror.Wrapf("invalid host: %w", err)
	}

	// Missing components are allowed for partial autofill.
	database := strings.TrimPrefix(u.Path, "/")

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, xerror.Unexpected().Detailf(
			"invalid port %q: must be between 1 and 65535",
			portStr,
		)
	}

	fields := &ParsedFields{
		Host:     host,
		Port:     port,
		Database: database,
		SSLMode:  u.Query().Get("sslmode"),
	}

	if u.User != nil {
		fields.Username = u.User.Username()
		fields.Password, _ = u.User.Password()
	}

	return fields, nil
}

// ParseMySQL parses a MySQL connection URI.
// Supported schemes: mysql.
// Example: mysql://user:pass@host:3306/dbname
func ParseMySQL(uri string) (*ParsedFields, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	if u.Scheme != "mysql" {
		return nil, ErrUnsupportedScheme.Detailf("%q (expected mysql)", u.Scheme)
	}

	host, portStr, err := splitHostPort(u.Host, "3306")
	if err != nil {
		return nil, xerror.Wrapf("invalid host: %w", err)
	}

	database := strings.TrimPrefix(u.Path, "/")

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, xerror.Unexpected().Detailf(
			"invalid port %q: must be between 1 and 65535",
			portStr,
		)
	}

	fields := &ParsedFields{
		Host:     host,
		Port:     port,
		Database: database,
	}

	if u.User != nil {
		fields.Username = u.User.Username()
		fields.Password, _ = u.User.Password()
	}

	return fields, nil
}

// ParseMongoDB parses a MongoDB connection URI.
// Supported schemes: mongodb, mongodb+srv.
// Example: mongodb://user:pass@host:27017/dbname?authSource=admin
func ParseMongoDB(uri string) (*ParsedFields, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	switch u.Scheme {
	case "mongodb", "mongodb+srv":
	default:
		return nil, ErrUnsupportedScheme.Detailf("%q (expected mongodb or mongodb+srv)", u.Scheme)
	}

	// mongodb+srv does not support explicit ports
	defaultPort := "27017"
	host, portStr, err := splitHostPort(u.Host, defaultPort)
	if err != nil {
		return nil, xerror.Wrapf("invalid host: %w", err)
	}

	database := strings.TrimPrefix(u.Path, "/")

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, xerror.Unexpected().Detailf(
			"invalid port %q: must be between 1 and 65535",
			portStr,
		)
	}

	fields := &ParsedFields{
		Host:         host,
		Port:         port,
		Database:     database,
		AuthDatabase: u.Query().Get("authSource"),
	}

	if u.User != nil {
		fields.Username = u.User.Username()
		fields.Password, _ = u.User.Password()
	}

	return fields, nil
}

// ParseHTTP parses an HTTP/HTTPS service URI.
// Supported schemes: http, https.
// The full URI (scheme + host + port) becomes BaseURL; the path becomes HealthPath.
// Example: https://api.example.com:8443/health
func ParseHTTP(uri string) (*ParsedFields, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	switch u.Scheme {
	case "http", "https":
	default:
		return nil, ErrUnsupportedScheme.Detailf("%q (expected http or https)", u.Scheme)
	}

	// BaseURL is scheme+host (no path)
	baseURL := ""
	if u.Host != "" {
		baseURL = u.Scheme + "://" + u.Host
	}

	healthPath := u.Path
	if healthPath == "" {
		healthPath = "/"
	}

	fields := &ParsedFields{
		BaseURL:    baseURL,
		HealthPath: healthPath,
	}

	// If basic auth credentials are embedded in the URI, surface them.
	if u.User != nil {
		fields.Username = u.User.Username()
		fields.Password, _ = u.User.Password()
	}

	return fields, nil
}

// splitHostPort splits a host string that may or may not contain a port.
// If no port is present the supplied defaultPort is used.
// It correctly handles IPv6 bracket notation (e.g. [::1]:5432).
func splitHostPort(hostport, defaultPort string) (host, port string, err error) {
	if hostport == "" {
		return "", defaultPort, nil
	}

	if strings.HasPrefix(hostport, "[") && !strings.Contains(hostport, "]") {
		return "", "", xerror.Unexpected().Detailf(
			"invalid host %q: malformed IPv6 brackets",
			hostport,
		)
	}

	if strings.HasSuffix(hostport, ":") {
		return "", "", xerror.Unexpected().Detailf("invalid host %q: missing port", hostport)
	}

	// net.SplitHostPort requires a port; try it first.
	h, p, splitErr := net.SplitHostPort(hostport)
	if splitErr == nil {
		if p == "" {
			p = defaultPort
		}
		return h, p, nil
	}

	// If the string looks like a bare IPv6 address in brackets, extract it.
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end != -1 {
			return hostport[1:end], defaultPort, nil
		}
	}

	// No port — treat the entire string as a hostname.
	return hostport, defaultPort, nil
}
