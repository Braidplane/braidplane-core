package dsparser_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/frontend/dsparser"
)

func TestParsePostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		uri             string
		want            *dsparser.ParsedFields
		wantErrContains string
	}{
		{
			name: "FullURI",
			uri:  "postgres://app_user:s3cret@db.example.com:5432/production",
			want: &dsparser.ParsedFields{
				Host:     "db.example.com",
				Port:     5432,
				Database: "production",
				Username: "app_user",
				Password: "s3cret",
			},
		},
		{
			name: "PostgresqlScheme",
			uri:  "postgresql://admin:pass@localhost:5432/mydb",
			want: &dsparser.ParsedFields{
				Host:     "localhost",
				Port:     5432,
				Database: "mydb",
				Username: "admin",
				Password: "pass",
			},
		},
		{
			name: "MissingPort_UsesDefault",
			uri:  "postgres://user:pass@host.local/mydb",
			want: &dsparser.ParsedFields{
				Host:     "host.local",
				Port:     5432,
				Database: "mydb",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "EncodedPassword",
			uri:  "postgres://user:p%40ss%21word@host:5432/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     5432,
				Database: "db",
				Username: "user",
				Password: "p@ss!word",
			},
		},
		{
			name: "WithSSLMode",
			uri:  "postgres://user:pass@host:5432/db?sslmode=require",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     5432,
				Database: "db",
				Username: "user",
				Password: "pass",
				SSLMode:  "require",
			},
		},
		{
			name: "IPv6Host",
			uri:  "postgres://user:pass@[::1]:5432/db",
			want: &dsparser.ParsedFields{
				Host:     "::1",
				Port:     5432,
				Database: "db",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "NoCreds",
			uri:  "postgres://host:5432/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     5432,
				Database: "db",
			},
		},
		{
			name:            "WrongScheme",
			uri:             "mysql://user:pass@host:3306/db",
			wantErrContains: "unsupported URI scheme",
		},
		{
			name: "MissingHost",
			uri:  "postgres:///db",
			want: &dsparser.ParsedFields{
				Host:     "",
				Port:     5432,
				Database: "db",
			},
		},
		{
			name: "MissingDatabase",
			uri:  "postgres://user:pass@host:5432/",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     5432,
				Database: "",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "MissingDatabaseNoSlash",
			uri:  "postgres://user:pass@host:5432",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     5432,
				Database: "",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name:            "TrailingColonInHost",
			uri:             "postgres://user:pass@host:/db",
			wantErrContains: "invalid host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dsparser.ParsePostgres(tt.uri)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
				require.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want.Host, got.Host, "Host mismatch")
			require.Equal(t, tt.want.Port, got.Port, "Port mismatch")
			require.Equal(t, tt.want.Database, got.Database, "Database mismatch")
			require.Equal(t, tt.want.Username, got.Username, "Username mismatch")
			require.Equal(t, tt.want.Password, got.Password, "Password mismatch")
			require.Equal(t, tt.want.SSLMode, got.SSLMode, "SSLMode mismatch")
		})
	}
}

func TestParseMySQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		uri             string
		want            *dsparser.ParsedFields
		wantErrContains string
	}{
		{
			name: "FullURI",
			uri:  "mysql://app:secret@mysql.example.com:3306/analytics",
			want: &dsparser.ParsedFields{
				Host:     "mysql.example.com",
				Port:     3306,
				Database: "analytics",
				Username: "app",
				Password: "secret",
			},
		},
		{
			name: "MissingPort_UsesDefault",
			uri:  "mysql://user:pass@host/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     3306,
				Database: "db",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "EncodedPassword",
			uri:  "mysql://user:p%40ss%23@host:3306/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     3306,
				Database: "db",
				Username: "user",
				Password: "p@ss#",
			},
		},
		{
			name: "IPv6Host",
			uri:  "mysql://user:pass@[::1]:3306/db",
			want: &dsparser.ParsedFields{
				Host:     "::1",
				Port:     3306,
				Database: "db",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "NoCreds",
			uri:  "mysql://host:3306/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     3306,
				Database: "db",
			},
		},
		{
			name:            "WrongScheme",
			uri:             "postgres://user:pass@host:5432/db",
			wantErrContains: "unsupported URI scheme",
		},
		{
			name: "MissingHost",
			uri:  "mysql:///db",
			want: &dsparser.ParsedFields{
				Host:     "",
				Port:     3306,
				Database: "db",
			},
		},
		{
			name: "MissingDatabase",
			uri:  "mysql://user:pass@host:3306/",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     3306,
				Database: "",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name:            "TrailingColonInHost",
			uri:             "mysql://user:pass@host:/db",
			wantErrContains: "invalid host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dsparser.ParseMySQL(tt.uri)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
				require.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want.Host, got.Host, "Host mismatch")
			require.Equal(t, tt.want.Port, got.Port, "Port mismatch")
			require.Equal(t, tt.want.Database, got.Database, "Database mismatch")
			require.Equal(t, tt.want.Username, got.Username, "Username mismatch")
			require.Equal(t, tt.want.Password, got.Password, "Password mismatch")
		})
	}
}

func TestParseMongoDB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		uri             string
		want            *dsparser.ParsedFields
		wantErrContains string
	}{
		{
			name: "FullURI",
			uri:  "mongodb://app_user:secret@mongodb.example.com:27017/app_data",
			want: &dsparser.ParsedFields{
				Host:     "mongodb.example.com",
				Port:     27017,
				Database: "app_data",
				Username: "app_user",
				Password: "secret",
			},
		},
		{
			name: "WithAuthSource",
			uri:  "mongodb://user:pass@host:27017/mydb?authSource=admin",
			want: &dsparser.ParsedFields{
				Host:         "host",
				Port:         27017,
				Database:     "mydb",
				Username:     "user",
				Password:     "pass",
				AuthDatabase: "admin",
			},
		},
		{
			name: "MissingPort_UsesDefault",
			uri:  "mongodb://user:pass@host/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     27017,
				Database: "db",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "SRVScheme",
			uri:  "mongodb+srv://user:pass@cluster.example.com/db",
			want: &dsparser.ParsedFields{
				Host:     "cluster.example.com",
				Port:     27017,
				Database: "db",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "SRVSchemeWithoutDatabase",
			uri:  "mongodb+srv://myDatabaseUser:D1fficultP%40ssw0rd@cluster0.example.mongodb.net/?retryWrites=true&w=majority",
			want: &dsparser.ParsedFields{
				Host:     "cluster0.example.mongodb.net",
				Port:     27017,
				Database: "",
				Username: "myDatabaseUser",
				Password: "D1fficultP@ssw0rd",
			},
		},
		{
			name: "EncodedPassword",
			uri:  "mongodb://user:p%40ssword%21@host:27017/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     27017,
				Database: "db",
				Username: "user",
				Password: "p@ssword!",
			},
		},
		{
			name: "IPv6Host",
			uri:  "mongodb://user:pass@[::1]:27017/db",
			want: &dsparser.ParsedFields{
				Host:     "::1",
				Port:     27017,
				Database: "db",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "NoCreds",
			uri:  "mongodb://host:27017/db",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     27017,
				Database: "db",
			},
		},
		{
			name:            "WrongScheme",
			uri:             "mysql://user:pass@host:3306/db",
			wantErrContains: "unsupported URI scheme",
		},
		{
			name: "MissingHost",
			uri:  "mongodb:///db",
			want: &dsparser.ParsedFields{
				Host:     "",
				Port:     27017,
				Database: "db",
			},
		},
		{
			name: "MissingDatabase",
			uri:  "mongodb://user:pass@host:27017/",
			want: &dsparser.ParsedFields{
				Host:     "host",
				Port:     27017,
				Database: "",
				Username: "user",
				Password: "pass",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dsparser.ParseMongoDB(tt.uri)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
				require.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want.Host, got.Host, "Host mismatch")
			require.Equal(t, tt.want.Port, got.Port, "Port mismatch")
			require.Equal(t, tt.want.Database, got.Database, "Database mismatch")
			require.Equal(t, tt.want.Username, got.Username, "Username mismatch")
			require.Equal(t, tt.want.Password, got.Password, "Password mismatch")
			require.Equal(t, tt.want.AuthDatabase, got.AuthDatabase, "AuthDatabase mismatch")
		})
	}
}

func TestParseHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		uri             string
		want            *dsparser.ParsedFields
		wantErrContains string
	}{
		{
			name: "HTTPSWithPath",
			uri:  "https://api.example.com/health",
			want: &dsparser.ParsedFields{
				BaseURL:    "https://api.example.com",
				HealthPath: "/health",
			},
		},
		{
			name: "HTTPNoPath",
			uri:  "http://internal-service:8080",
			want: &dsparser.ParsedFields{
				BaseURL:    "http://internal-service:8080",
				HealthPath: "/",
			},
		},
		{
			name: "HTTPSWithPort",
			uri:  "https://api.example.com:8443/health",
			want: &dsparser.ParsedFields{
				BaseURL:    "https://api.example.com:8443",
				HealthPath: "/health",
			},
		},
		{
			name: "HTTPSWithBasicAuth",
			uri:  "https://user:pass@api.example.com/health",
			want: &dsparser.ParsedFields{
				BaseURL:    "https://api.example.com",
				HealthPath: "/health",
				Username:   "user",
				Password:   "pass",
			},
		},
		{
			name: "HTTPSDeepPath",
			uri:  "https://api.example.com/v2/status",
			want: &dsparser.ParsedFields{
				BaseURL:    "https://api.example.com",
				HealthPath: "/v2/status",
			},
		},
		{
			name: "IPv6Host",
			uri:  "http://[::1]:9000/ping",
			want: &dsparser.ParsedFields{
				BaseURL:    "http://[::1]:9000",
				HealthPath: "/ping",
			},
		},
		{
			name:            "WrongScheme",
			uri:             "postgres://host/db",
			wantErrContains: "unsupported URI scheme",
		},
		{
			name: "MissingHost",
			uri:  "https:///health",
			want: &dsparser.ParsedFields{
				BaseURL:    "",
				HealthPath: "/health",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dsparser.ParseHTTP(tt.uri)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
				require.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want.BaseURL, got.BaseURL, "BaseURL mismatch")
			require.Equal(t, tt.want.HealthPath, got.HealthPath, "HealthPath mismatch")
			require.Equal(t, tt.want.Username, got.Username, "Username mismatch")
			require.Equal(t, tt.want.Password, got.Password, "Password mismatch")
		})
	}
}
