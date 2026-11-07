package snowflakewif

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/proto/spiffe/workload"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWorkloadAPIRequestsExactIdentityAndRenews(t *testing.T) {
	t.Parallel()
	socket := workloadTestSocket(t)
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "unix", socket)
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	fake := &workloadServer{key: key, revoked: map[string]bool{}}
	server := grpc.NewServer()
	workload.RegisterSpiffeWorkloadAPIServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	identity := func(org string) Identity {
		subject, err := spiffeid.FromString("spiffe://issuer.test/tenant/" + org + "/snowflake/one/connection/ds")
		require.NoError(t, err)
		return Identity{Issuer: "https://issuer.test", Subject: subject, Audience: "https://one.snowflakecomputing.com"}
	}
	alpha, beta := identity("alpha"), identity("beta")
	first, err := Token(t.Context(), socket, alpha)
	require.NoError(t, err)
	second, err := Token(t.Context(), socket, alpha)
	require.NoError(t, err)
	require.NotEqual(t, first, second, "new authentication must contact the issuer again")
	_, err = Token(t.Context(), socket, beta)
	require.NoError(t, err)
	fake.mu.Lock()
	require.Equal(t, []string{alpha.Subject.String(), alpha.Subject.String(), beta.Subject.String()}, fake.subjects)
	require.Equal(t, []string{alpha.Audience, alpha.Audience, beta.Audience}, fake.audiences)
	fake.revoked[alpha.Subject.String()] = true
	fake.mu.Unlock()
	_, err = Token(t.Context(), socket, alpha)
	require.ErrorContains(t, err, "runtime identity is unavailable or invalid")
	require.NotContains(t, err.Error(), "upstream-secret-canary")
	_, err = Token(t.Context(), socket, beta)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Token(ctx, socket, beta)
	require.ErrorContains(t, err, "runtime identity is unavailable or invalid")
}

// Keep sockets within the dedicated Codex allowlist and below macOS's path limit.
func workloadTestSocket(t *testing.T) string {
	t.Helper()
	root := "/tmp/braidplane-wif-sockets"
	if runtime.GOOS == "darwin" {
		root = "/private/tmp/braidplane-wif-sockets"
	}
	require.NoError(t, os.MkdirAll(root, 0o700))
	dir, err := os.MkdirTemp(root, "wif-") //nolint:usetesting // t.TempDir is outside the socket allowlist and may exceed macOS's path limit.
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return filepath.Join(dir, "api.sock")
}

func TestWorkloadTestSocketsAreIsolatedAndCleanedUp(t *testing.T) {
	t.Parallel()
	sockets := make(chan string, 4)
	t.Run("concurrent", func(t *testing.T) {
		for range 4 {
			t.Run("socket", func(t *testing.T) {
				t.Parallel()
				socket := workloadTestSocket(t)
				var config net.ListenConfig
				listener, err := config.Listen(t.Context(), "unix", socket)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, listener.Close()) })
				sockets <- socket
			})
		}
	})
	close(sockets)
	seen := make(map[string]bool)
	for socket := range sockets {
		dir := filepath.Dir(socket)
		require.False(t, seen[dir], "concurrent tests must use separate directories")
		seen[dir] = true
		_, err := os.Stat(dir)
		require.True(t, os.IsNotExist(err), "test cleanup must remove socket directory: %v", err)
	}
	require.Len(t, seen, 4)
}

type workloadServer struct {
	workload.UnimplementedSpiffeWorkloadAPIServer
	mu        sync.Mutex
	key       *ecdsa.PrivateKey
	revoked   map[string]bool
	subjects  []string
	audiences []string
}

func (s *workloadServer) FetchJWTSVID(_ context.Context, request *workload.JWTSVIDRequest) (*workload.JWTSVIDResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked[request.SpiffeId] || len(request.Audience) != 1 {
		return nil, status.Error(codes.PermissionDenied, "upstream-secret-canary")
	}
	s.subjects = append(s.subjects, request.SpiffeId)
	s.audiences = append(s.audiences, request.Audience[0])
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: s.key, KeyID: "test"}}, nil)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: "https://issuer.test", Subject: request.SpiffeId, Audience: request.Audience,
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Serialize()
	if err != nil {
		return nil, err
	}
	return &workload.JWTSVIDResponse{Svids: []*workload.JWTSVID{{SpiffeId: request.SpiffeId, Svid: token}}}, nil
}

func (s *workloadServer) FetchJWTBundles(_ *workload.JWTBundlesRequest, stream grpc.ServerStreamingServer[workload.JWTBundlesResponse]) error {
	bundle, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &s.key.PublicKey, KeyID: "test", Algorithm: "ES256"}}})
	if err != nil {
		return err
	}
	return stream.Send(&workload.JWTBundlesResponse{Bundles: map[string][]byte{"spiffe://issuer.test": bundle}})
}
