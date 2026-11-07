//go:build linux

package repostore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/stretchr/testify/require"
)

func TestNativeGitLargeFileQualification(t *testing.T) {
	if os.Getenv("BP_NATIVE_GIT_QUALIFICATION") != "1" {
		t.Skip("set BP_NATIVE_GIT_QUALIFICATION=1 for isolated large-file Git qualification")
	}
	revision := os.Getenv("BP_NATIVE_FILE_REVISION")
	decoded, err := hex.DecodeString(revision)
	require.NoError(t, err)
	require.Len(t, decoded, 20)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	size := int64(20<<20 + 17)
	if raw := os.Getenv("BP_NATIVE_GIT_BYTES"); raw != "" {
		size, err = strconv.ParseInt(raw, 10, 64)
		require.NoError(t, err)
	}
	require.GreaterOrEqual(t, size, int64(17))
	require.LessOrEqual(t, size, int64(2<<30), "qualification scratch use is capped; extend this fixture explicitly for larger samples")
	source, remote := t.TempDir(), t.TempDir()
	initSourceRepo(t, ctx, source, map[string]string{"marker.txt": "prior-good"})
	initBareRemote(t, remote)
	// The local fixture also acts as the server; keep its pack generation streaming.
	for _, directory := range []string{source, remote} {
		runGitTest(t, ctx, directory, "config", "core.bigFileThreshold", "1m")
		runGitTest(t, ctx, directory, "config", "core.packedGitWindowSize", "1m")
		runGitTest(t, ctx, directory, "config", "core.packedGitLimit", "8m")
		runGitTest(t, ctx, directory, "config", "pack.windowMemory", "8m")
		runGitTest(t, ctx, directory, "config", "receive.unpackLimit", "1")
		runGitTest(t, ctx, directory, "config", "pack.threads", "1")
	}
	seedPath := filepath.Join(source, "source.bin")
	seed, err := os.Create(seedPath)
	require.NoError(t, err)
	sourceHash := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(seed, sourceHash), io.LimitReader(rand.Reader, size), make([]byte, 64<<10))
	require.NoError(t, err)
	require.Equal(t, size, n)
	require.NoError(t, seed.Close())
	runGitTest(t, ctx, source, "add", "--", "source.bin")
	runGitTest(t, ctx, source, "commit", "-m", "random native file fixture")
	runGitTest(t, ctx, source, "push", remote, "HEAD:main")
	runGitTest(t, ctx, remote, "config", "http.receivepack", "true")
	server, env := nativeGitHTTPFixture(t, remote)
	remoteURL := server.URL + "/" + filepath.Base(remote)
	limited, err := repostore.WithNativeFileLimits(ctx, size)
	require.NoError(t, err)
	started := time.Now()
	reader, readRoot, readCleanup, err := repostore.CloneNativeFileRemote(limited, remoteURL, "main", "source.bin", size, env)
	require.NoError(t, err)
	t.Cleanup(readCleanup)
	readHash := sha256.New()
	metadata, err := reader.(repostore.StreamingStore).ReadFileTo(limited, "source.bin", size, readHash)
	require.NoError(t, err)
	require.Equal(t, size, metadata.SizeBytes)
	require.Equal(t, sourceHash.Sum(nil), readHash.Sum(nil))
	input, err := os.Open(seedPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, input.Close()) }()
	var last [1]byte
	_, err = input.ReadAt(last[:], size-1)
	require.NoError(t, err)
	last[0] ^= 0xff
	body := func() io.Reader {
		return io.MultiReader(io.NewSectionReader(input, 0, size-1), bytes.NewReader(last[:]))
	}
	expectedHash := sha256.New()
	_, err = io.Copy(expectedHash, body())
	require.NoError(t, err)
	ref := filepayload.Reference{ID: "ea946443-7b39-4f64-8faf-eb3fac36c17f", SizeBytes: size, SHA256: hex.EncodeToString(expectedHash.Sum(nil))}
	writer, writeRoot, writeCleanup, err := repostore.CloneNativeFileRemote(limited, remoteURL, "main", "destination.bin", size, env)
	require.NoError(t, err)
	t.Cleanup(writeCleanup)
	require.NoError(t, writer.(repostore.StreamingStore).WriteFileFrom(limited, "destination.bin", body(), ref, false, "new native blob"))
	require.NoError(t, repostore.PushRemote(limited, writeRoot, remoteURL, env, repostore.PushOptions{}))
	elapsed := time.Since(started)
	var children syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children))
	oracleHash := sha256.New()
	count := &nativeGitByteCounter{}
	oracle := exec.CommandContext(ctx, "git", "cat-file", "blob", "refs/heads/main:destination.bin")
	oracle.Dir = remote
	oracle.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	oracle.Stdout = io.MultiWriter(oracleHash, count)
	require.NoError(t, oracle.Run())
	require.Equal(t, size, count.bytes)
	require.Equal(t, expectedHash.Sum(nil), oracleHash.Sum(nil))
	storage := func(root string) (total, largest int64) {
		require.NoError(t, filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
			require.NoError(t, err)
			info, err := entry.Info()
			require.NoError(t, err)
			if info.Mode().IsRegular() {
				total += info.Size()
				largest = max(largest, info.Size())
			}
			return nil
		}))
		return total, largest
	}
	readBytes, readLargest := storage(readRoot)
	writeBytes, writeLargest := storage(writeRoot)
	version, err := exec.CommandContext(ctx, "git", "--version").Output()
	require.NoError(t, err)
	var self syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &self))
	evidence, err := json.Marshal(map[string]any{
		"revision": revision, "bytes": size, "sha256": ref.SHA256, "elapsed_seconds": elapsed.Seconds(),
		"go_version": runtime.Version(), "git_version": string(bytes.TrimSpace(version)),
		"process_address_space_limit_bytes": 512 << 20, "single_git_allocation_limit_bytes": 16 << 20,
		"git_file_write_limit_bytes":  ((2*size + (64 << 20) + 511) / 512) * 512,
		"read_checkout_logical_bytes": readBytes, "write_checkout_logical_bytes": writeBytes,
		"largest_checkout_file_bytes":    max(readLargest, writeLargest),
		"go_test_process_peak_rss_bytes": self.Maxrss * 1024, "qualification_largest_reaped_child_peak_rss_bytes": children.Maxrss * 1024,
		"components": "native Git clone/read/write/push with kernel limits; separate TLS CGI Git server with disk-spooled chunked requests and retained packs; disk random seed and independent full Git readback; Go RSS includes fixture/oracle, child RSS includes seed/server/client before oracle and is largest reaped child, not aggregate; broker/retention/API excluded",
	})
	require.NoError(t, err)
	t.Logf("NATIVE_GIT_QUALIFICATION_EVIDENCE %s", evidence)
}

func nativeGitHTTPFixture(t *testing.T, remote string) (*httptest.Server, []string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)
	spool := t.TempDir()
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Root: "/", Dir: filepath.Dir(remote), Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(remote), "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, Stderr: os.Stderr}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 0 {
			// CGI requires a known request length; the provider fixture spools to
			// disk while the independently limited Git client keeps streaming.
			body, err := os.CreateTemp(spool, "request-*")
			if err != nil {
				http.Error(w, "fixture spool failed", http.StatusInternalServerError)
				return
			}
			defer func() { _ = body.Close(); _ = os.Remove(body.Name()) }()
			n, err := io.Copy(body, io.LimitReader(r.Body, (3<<30)+1))
			if err != nil || n > 3<<30 {
				http.Error(w, "fixture request too large or incomplete", http.StatusRequestEntityTooLarge)
				return
			}
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				http.Error(w, "fixture rewind failed", http.StatusInternalServerError)
				return
			}
			r = r.Clone(r.Context())
			r.ContentLength = n
			r.TransferEncoding = nil
			r.Header.Del("Transfer-Encoding")
			r.Body = body
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(spool, "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	return server, []string{"GIT_SSL_CAINFO=" + ca}
}

type nativeGitByteCounter struct{ bytes int64 }

func (c *nativeGitByteCounter) Write(p []byte) (int, error) {
	c.bytes += int64(len(p))
	return len(p), nil
}
