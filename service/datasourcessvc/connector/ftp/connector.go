package ftp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/textproto"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	ftpclient "github.com/jlaffaye/ftp"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// Config holds configuration for FTP connections.
type Config struct {
	Host              string `json:"host"                          validate:"required,hostname|ip"`
	Port              int    `json:"port"                          validate:"required,min=1,max=65535"`
	BasePath          string `json:"base_path,omitempty"`
	ConnectTimeout    int    `json:"connect_timeout,omitempty"     validate:"omitempty,min=1,max=300"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds FTP authentication values.
type Credentials struct {
	Username string `json:"username" validate:"required,min=1"`
	Password string `json:"password" validate:"required,min=1"`
}

// Connector connects to FTP servers and executes file-backed row queries.
type Connector struct {
	conn     *ftpclient.ServerConn
	basePath string

	operation    chan struct{}
	mu           sync.Mutex
	operationCtx context.Context
	control      net.Conn
	data         net.Conn
	closed       bool
}

// QueryFormat is an alias for filedata.QueryFormat.
type QueryFormat = filedata.QueryFormat

const (
	QueryFormatUnknown     = filedata.QueryFormatUnknown
	QueryFormatAuto        = filedata.QueryFormatAuto
	QueryFormatCSV         = filedata.QueryFormatCSV
	QueryFormatJSON        = filedata.QueryFormatJSON
	QueryFormatFile        = filedata.QueryFormatFile
	QueryFormatSpreadsheet = filedata.QueryFormatSpreadsheet
)

type ftpQueryData = remotefile.Statement

const (
	ftpOperationRead  = remotefile.OperationRead
	ftpOperationWrite = remotefile.OperationWrite
)

var ftpFileAdapter = remotefile.Adapter{
	ConnectorName: "FTP",
	LimitPolicy: remotefile.LimitPolicy{
		UseHTTPFallbackForRead: true,
	},
}

// New creates a new FTP connector.
func New(ctx context.Context, config Config, creds Credentials) (*Connector, error) {
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 5
	}

	connectTimeout := time.Duration(config.ConnectTimeout) * time.Second
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	policy := hostguard.NewDestinationPolicy(config.AllowPrivateHosts)
	if _, err := policy.Resolve(connectCtx, config.Host); err != nil {
		return nil, xerror.Wrapf("connect to FTP server: %w", err)
	}

	addr := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	dialer := &net.Dialer{Timeout: connectTimeout}
	connector := &Connector{operation: make(chan struct{}, 1)}
	var control net.Conn
	var stopHandshake func() bool
	defer func() {
		if stopHandshake != nil {
			stopHandshake()
		}
	}()
	dial := func(network, address string) (net.Conn, error) {
		if control != nil {
			return connector.dialData(policy, dialer, network, address)
		}
		socket, err := ftpDialFunc(connectCtx, policy, dialer)(network, address)
		if err != nil {
			return nil, err
		}
		deadline, _ := connectCtx.Deadline()
		if err := socket.SetDeadline(deadline); err != nil {
			_ = socket.Close()
			return nil, xerror.Wrapf("set FTP control connection deadline: %w", err)
		}
		control = socket
		stopHandshake = context.AfterFunc(connectCtx, func() { _ = socket.Close() })
		return socket, nil
	}
	options := []ftpclient.DialOption{
		ftpclient.DialWithContext(ctx),
		ftpclient.DialWithTimeout(connectTimeout),
	}
	if !transportsecurity.PlaintextAllowed(ctx) {
		tlsConfig := transportsecurity.TLS(config.Host)
		options = append(options, ftpclient.DialWithExplicitTLS(tlsConfig))
		dial = ftpTLSDataDialFunc(dial, tlsConfig)
	}
	options = append(options, ftpclient.DialWithDialFunc(dial))
	conn, err := ftpclient.Dial(
		addr, options...,
	)
	if err != nil {
		return nil, mapConnectionError(err)
	}

	if err := conn.Login(creds.Username, creds.Password); err != nil {
		_ = conn.Quit()
		return nil, mapConnectionError(err)
	}

	if !stopHandshake() {
		_ = control.Close()
		return nil, mapConnectionError(connectCtx.Err())
	}
	if err := control.SetDeadline(time.Time{}); err != nil {
		_ = control.Close()
		return nil, mapConnectionError(err)
	}

	basePath := strings.TrimSpace(config.BasePath)
	if basePath == "" {
		basePath = "/"
	}

	connector.conn, connector.basePath, connector.control = conn, basePath, control
	return connector, nil
}

// ftpDialFunc adapts the shared destination policy to both FTP control and passive data dials.
func ftpDialFunc(ctx context.Context, policy hostguard.DestinationPolicy, dialer *net.Dialer) func(string, string) (net.Conn, error) {
	return ftpDialFuncWithLookup(ctx, policy, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

// ftpDialFuncWithLookup is the testable FTP dial adapter and never passes a hostname to dial.
func ftpDialFuncWithLookup(
	ctx context.Context,
	policy hostguard.DestinationPolicy,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(string, string) (net.Conn, error) {
	return func(network, address string) (net.Conn, error) {
		return policy.DialContextWithDialer(ctx, network, address, lookup, dial)
	}
}

// TestConnection verifies that a connection to the FTP server is still alive.
func (c *Connector) TestConnection(ctx context.Context) (err error) {
	finish, err := c.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if ctxErr := finish(); ctxErr != nil {
			err = mapConnectionError(ctxErr)
		}
	}()

	if err := c.conn.NoOp(); err != nil {
		return mapConnectionError(err)
	}

	return nil
}

func mapConnectionError(err error) error {
	var protocolErr *textproto.Error
	if xerror.As(err, &protocolErr) && protocolErr.Code == ftpclient.StatusNotLoggedIn {
		return xerror.InvalidCredentials().With(err)
	}

	return xerror.ConnectionFailed(xerror.DataSource).With(err)
}

// Close closes the FTP connection.
func (c *Connector) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}

	if err := c.closeSockets(); err != nil && !xerror.Is(err, net.ErrClosed) {
		return xerror.Unexpected().With(err)
	}

	return nil
}

// Execute runs an FTP file operation (read or write) and returns an FTP execution result.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	execLimits := types.ResolveExecutionLimits(limits...)

	start := time.Now()

	normalized, err := types.NormalizeParams(params)
	if err != nil {
		duration := time.Since(start).Milliseconds()
		return result.NewFTPResult(
			nil,
			duration,
			xerror.Wrapf("parameter normalization failed: %w", err),
		), nil
	}

	qd, err := remotefile.DecodeStatement(queryData)
	if err != nil {
		duration := time.Since(start).Milliseconds()
		return result.NewFTPResult(
			nil,
			duration,
			xerror.InvalidInput(xerror.Query).Detailf("invalid FTP operation: %w", err),
		), nil
	}

	operation := remotefile.OperationOrDefault(qd.Operation)
	if operation == remotefile.OperationWrite {
		applyWriteParamOverrides(&qd, normalized)
	}

	if strings.TrimSpace(qd.Path) == "" {
		duration := time.Since(start).Milliseconds()
		return result.NewFTPResult(
			nil,
			duration,
			xerror.Unexpected().Detail("FTP query data must specify a path"),
		), nil
	}

	if err := substituteSpreadsheetParams(&qd, normalized); err != nil {
		duration := time.Since(start).Milliseconds()
		return result.NewFTPResult(nil, duration, err), nil
	}

	joinedPath, err := joinFTPPath(c.basePath, qd.Path)
	if err != nil {
		duration := time.Since(start).Milliseconds()
		return result.NewFTPResult(
			nil,
			duration,
			xerror.Wrapf("invalid FTP path: %w", err),
		), nil
	}

	ftpResult, err := ftpFileAdapter.ExecuteOperation(
		ctx,
		ftpProvider{connector: c},
		joinedPath,
		qd,
		execLimits,
	)
	if err != nil {
		duration := time.Since(start).Milliseconds()
		return result.NewFTPResult(nil, duration, err), nil
	}

	duration := time.Since(start).Milliseconds()
	return result.NewFTPResult(ftpResult, duration, nil), nil
}

// applyWriteParamOverrides applies shared FTP sink runtime overrides.
func applyWriteParamOverrides(qd *ftpQueryData, params map[string]any) {
	remotefile.ApplyWriteParamOverrides(
		qd,
		params,
		remotefile.WriteOverrideOptions{TargetParamName: "path"},
	)
}

// substituteSpreadsheetParams resolves sheet and range placeholders for spreadsheet operations.
func substituteSpreadsheetParams(qd *ftpQueryData, params map[string]any) error {
	return remotefile.SubstituteCommonParams(qd, params, "path")
}

func joinFTPPath(basePath, relativePath string) (string, error) {
	baseClean := path.Clean("/" + strings.TrimSpace(basePath))
	rel := relativePath
	if rel == "" {
		return baseClean, nil
	}
	if strings.HasPrefix(rel, "/") {
		return "", xerror.Unexpected().Detail("absolute paths are not allowed")
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == ".." {
			return "", xerror.Unexpected().Detail("path traversal is not allowed")
		}
	}

	joined := path.Clean(path.Join(baseClean, rel))
	if baseClean == "/" {
		return joined, nil
	}
	if joined != baseClean && !strings.HasPrefix(joined, baseClean+"/") {
		return "", xerror.Unexpected().Detail("path escapes configured base path")
	}

	return joined, nil
}

// executeWrite runs a shared remote-file write through the FTP provider.
func (c *Connector) executeWrite(targetPath string, qd ftpQueryData) (*result.FileResult, error) {
	return ftpFileAdapter.ExecuteWrite(context.Background(), ftpProvider{connector: c}, targetPath, qd)
}

func (c *Connector) fileExists(path string) (bool, error) {
	reader, err := c.conn.Retr(path)
	if err != nil {
		errMessage := strings.ToLower(err.Error())
		if strings.Contains(errMessage, "550") ||
			strings.Contains(errMessage, "not found") ||
			strings.Contains(errMessage, "no such file") {
			return false, nil
		}
		return false, xerror.Unexpected().With(err)
	}
	if err := reader.Close(); err != nil {
		// RETR already proved existence; retire a session with an incomplete final reply.
		_ = c.closeSockets()
	}
	return true, nil
}

// serializeWriteContent converts FTP write content through the shared remote-file adapter.
func serializeWriteContent(qd ftpQueryData, path string) ([]byte, QueryFormat, int, error) {
	return ftpFileAdapter.SerializeWriteContent(qd, path)
}

type ftpProvider struct {
	connector *Connector
}

// ReadFile retrieves bytes from FTP while keeping protocol errors in the provider boundary.
func (provider ftpProvider) ReadFile(
	ctx context.Context,
	target string,
	maxBytes int64,
) (readResult *remotefile.ReadResult, err error) {
	finish, err := provider.connector.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if ctxErr := finish(); ctxErr != nil {
			readResult, err = nil, xerror.Unexpected().With(ctxErr)
		}
	}()
	reader, err := provider.connector.conn.Retr(target)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			_ = provider.connector.closeSockets()
			if err == nil {
				readResult, err = nil, xerror.Unexpected().With(closeErr)
			}
		}
	}()

	content, err := remotefile.ReadAllLimited(reader, maxBytes)
	if err != nil {
		return nil, err
	}

	return &remotefile.ReadResult{Content: content}, nil
}

// WriteFile stores bytes on FTP and enforces FTP overwrite semantics.
func (provider ftpProvider) WriteFile(
	ctx context.Context,
	req remotefile.WriteRequest,
) (writeResult *remotefile.WriteResult, err error) {
	finish, err := provider.connector.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if ctxErr := finish(); ctxErr != nil {
			writeResult, err = nil, xerror.Unexpected().With(ctxErr)
		}
	}()
	if !req.Overwrite {
		exists, err := provider.connector.fileExists(req.Target)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, xerror.Unexpected().Detailf("target FTP file %q already exists", req.Target)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := provider.connector.conn.Stor(req.Target, bytes.NewReader(req.Content)); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &remotefile.WriteResult{WrittenBytes: len(req.Content)}, nil
}

// ftpTLSDataDialFunc preserves the control upgrade while encrypting every passive data socket.
// The FTP library bypasses data-channel TLS when a custom dial function is supplied.
func ftpTLSDataDialFunc(dial func(string, string) (net.Conn, error), tlsConfig *tls.Config) func(string, string) (net.Conn, error) {
	control := true
	return func(network, address string) (net.Conn, error) {
		conn, err := dial(network, address)
		if err != nil {
			return nil, err
		}
		if control {
			control = false
			return conn, nil
		}
		return tls.Client(conn, tlsConfig), nil
	}
}
