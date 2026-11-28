package ftpartifact

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// ftpRenameProxy forwards the disposable provider's control channel. Passive
// data stays on the fixture's own loopback port. Dropping the server reply only
// after a positive RNTO response models publication with a lost acknowledgement.
type ftpRenameProxy struct {
	port           int
	dropNextRename atomic.Bool
	renames        atomic.Int64
}

func startFTPRenameProxy(t *testing.T, providerPort int) *ftpRenameProxy {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy := &ftpRenameProxy{port: listener.Addr().(*net.TCPAddr).Port}
	ctx, cancel := context.WithCancel(t.Context())
	var all sync.WaitGroup
	all.Go(func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			all.Go(func() { proxy.forward(ctx, client, fmt.Sprintf("127.0.0.1:%d", providerPort)) })
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); all.Wait() })
	return proxy
}

func (p *ftpRenameProxy) forward(ctx context.Context, client net.Conn, providerAddr string) {
	defer func() { _ = client.Close() }()
	server, err := new(net.Dialer).DialContext(ctx, "tcp", providerAddr)
	if err != nil {
		return
	}
	closeBoth := func() { _ = client.Close(); _ = server.Close() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	var renaming atomic.Bool
	var commands sync.WaitGroup
	commands.Go(func() {
		defer closeBoth()
		req := bufio.NewReader(client)
		for {
			line, err := req.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "RNTO ") {
				renaming.Store(true)
			}
			if _, err := io.WriteString(server, line); err != nil {
				return
			}
		}
	})
	resp := bufio.NewReader(server)
	for {
		line, err := resp.ReadString('\n')
		if err != nil {
			break
		}
		terminal := len(line) >= 4 && line[3] == ' ' && line[0] >= '2' && line[0] <= '5'
		if terminal && renaming.Swap(false) && strings.HasPrefix(line, "250 ") {
			p.renames.Add(1)
			if p.dropNextRename.Swap(false) {
				break
			}
		}
		if _, err := io.WriteString(client, line); err != nil {
			break
		}
	}
	closeBoth()
	commands.Wait()
}
