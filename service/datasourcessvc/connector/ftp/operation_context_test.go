package ftp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

func TestFTPOperationCancellationInterruptsStalledIO(t *testing.T) {
	for _, stage := range []string{"NOOP", "RETR-start", "RETR-data", "RETR-final", "STOR-data", "STOR-final", "exists-final", "LIST-data"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			stall := stage
			if stage == "exists-final" {
				stall = "RETR-final"
			}
			connector, reached, release := newCancellationFTP(t, t.Context(), stall)
			defer func() { _ = connector.Close() }()
			defer release()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch {
				case stage == "NOOP":
					err = connector.TestConnection(ctx)
				case strings.HasPrefix(stage, "RETR"):
					_, err = (ftpProvider{connector}).ReadFile(ctx, "/fixture", 64)
				case stage == "exists-final":
					_, err = (ftpProvider{connector}).WriteFile(ctx, remotefile.WriteRequest{Target: "/fixture", Content: []byte("replacement")})
				case strings.HasPrefix(stage, "STOR"):
					content := []byte("fixture")
					if stage == "STOR-data" {
						content = []byte(strings.Repeat("x", 4<<20))
					}
					_, err = (ftpProvider{connector}).WriteFile(ctx, remotefile.WriteRequest{Target: "/fixture", Content: content, Overwrite: true})
				default:
					_, err = connector.IntrospectSchema(ctx)
				}
				done <- err
			}()
			select {
			case <-reached:
			case err := <-done:
				t.Fatalf("operation returned before the owned server stalled: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("operation did not reach the owned server stall")
			}
			cancel()
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Error("cancellation did not interrupt established FTP I/O")
				release()
				select {
				case err = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("owned FTP operation did not finish after server release")
				}
			}
			require.ErrorIs(t, err, context.Canceled)
			release()
			require.Error(t, connector.TestConnection(t.Context()), "a canceled protocol session must not be reused")
		})
	}
}

func TestFTPOperationDeadlineInterruptsControlReply(t *testing.T) {
	connector, reached, release := newCancellationFTP(t, t.Context(), "NOOP")
	defer func() { _ = connector.Close() }()
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- connector.TestConnection(ctx) }()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("NOOP did not reach the owned stall")
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Error("operation deadline did not interrupt the control reply")
		release()
		<-done
	}
}

func TestFTPOperationsKeepSuccessfulSessionIndependentOfSetupContext(t *testing.T) {
	setupCtx, cancelSetup := context.WithCancel(t.Context())
	connector, _, release := newCancellationFTP(t, setupCtx, "")
	defer func() { _ = connector.Close() }()
	defer release()
	cancelSetup()
	ctx, cancel := context.WithCancel(t.Context())
	for range 2 {
		got, err := (ftpProvider{connector}).ReadFile(ctx, "/fixture", 64)
		require.NoError(t, err)
		require.Equal(t, []byte("fixture"), got.Content)
	}
	cancel()
	require.NoError(t, connector.TestConnection(t.Context()), "a completed operation must detach its cancellation callback")
	alreadyCanceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, connector.TestConnection(alreadyCanceled), context.Canceled)
	require.NoError(t, connector.TestConnection(t.Context()), "a canceled waiter must not retire an idle session")
}

func TestFTPWaitingCallerCanCancelWithoutClosingActiveOperation(t *testing.T) {
	connector, reached, release := newCancellationFTP(t, t.Context(), "NOOP")
	defer func() { _ = connector.Close() }()
	defer release()
	first := make(chan error, 1)
	go func() { first <- connector.TestConnection(t.Context()) }()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("first operation did not reach the owned stall")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	second := make(chan error, 1)
	go func() { second <- connector.TestConnection(ctx) }()
	select {
	case err := <-second:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Error("a canceled waiter did not leave the operation queue")
		release()
		<-second
	}
	select {
	case err := <-first:
		t.Fatalf("canceling a waiter interrupted the active operation: %v", err)
	default:
	}
	release()
	require.NoError(t, <-first)
	require.NoError(t, connector.TestConnection(t.Context()))
}

func TestFTPReadRejectsFailedFinalReply(t *testing.T) {
	connector, _, release := newCancellationFTP(t, t.Context(), "RETR-error")
	defer func() { _ = connector.Close() }()
	defer release()
	got, err := (ftpProvider{connector}).ReadFile(t.Context(), "/fixture", 64)
	require.Error(t, err)
	require.Nil(t, got)
	require.Error(t, connector.TestConnection(t.Context()))
}

func TestFTPExistenceProbeKeepsAlreadyExistsAfterAbortedTransfer(t *testing.T) {
	connector, _, release := newCancellationFTP(t, t.Context(), "RETR-error")
	defer func() { _ = connector.Close() }()
	defer release()
	got, err := (ftpProvider{connector}).WriteFile(t.Context(), remotefile.WriteRequest{Target: "/fixture", Content: []byte("replacement")})
	require.ErrorContains(t, err, "already exists")
	require.Nil(t, got)
	require.Error(t, connector.TestConnection(t.Context()), "an unread final reply must not carry into another operation")
}

// newCancellationFTP owns a minimal loopback server with a releasable protocol stall.
func newCancellationFTP(t *testing.T, setupCtx context.Context, stall string) (*Connector, <-chan struct{}, func()) {
	t.Helper()
	serverCtx, cancelServer := context.WithCancel(context.WithoutCancel(setupCtx))
	listener, err := (&net.ListenConfig{}).Listen(serverCtx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	reached, released, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	accepted := make(chan net.Conn, 1)
	var releaseOnce, stallOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	wait := func(stage string) {
		if stage == stall {
			stallOnce.Do(func() { close(reached) })
			<-released
		}
	}
	t.Cleanup(func() {
		cancelServer()
		release()
		_ = listener.Close()
		select {
		case socket := <-accepted:
			_ = socket.Close()
		default:
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("owned FTP server did not stop")
		}
	})
	go func(ctx context.Context) {
		defer close(done)
		control, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- control
		defer func() { _ = control.Close() }()
		_ = control.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.WriteString(control, "220 synthetic FTP\r\n")
		reader := bufio.NewReader(control)
		var passive net.Listener
		defer func() {
			if passive != nil {
				_ = passive.Close()
			}
		}()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.Fields(line)[0]
			switch command {
			case "USER":
				_, _ = io.WriteString(control, "230 logged in\r\n")
			case "FEAT":
				_, _ = io.WriteString(control, "211 no features\r\n")
			case "EPSV":
				passive, err = (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
				if err != nil {
					return
				}
				_, port, _ := net.SplitHostPort(passive.Addr().String())
				_, _ = fmt.Fprintf(control, "229 passive (|||%s|)\r\n", port)
			case "RETR", "STOR", "LIST":
				if passive == nil {
					return
				}
				data, err := passive.Accept()
				_ = passive.Close()
				passive = nil
				if err != nil {
					return
				}
				stopData := context.AfterFunc(ctx, func() { _ = data.Close() })
				_ = data.SetDeadline(time.Now().Add(10 * time.Second))
				if tcp, ok := data.(*net.TCPConn); ok {
					_ = tcp.SetReadBuffer(1024)
				}
				wait(command + "-start")
				_, _ = io.WriteString(control, "150 data ready\r\n")
				if command == "STOR" {
					if stall == "STOR-data" {
						_, _ = io.CopyN(io.Discard, data, 1)
					}
					wait("STOR-data")
					if tcp, ok := data.(*net.TCPConn); ok {
						_ = tcp.SetReadBuffer(1 << 20)
					}
					_, _ = io.Copy(io.Discard, data)
				} else {
					wait(command + "-data")
					if command == "RETR" {
						_, _ = io.WriteString(data, "fixture")
					}
				}
				_ = data.Close()
				stopData()
				wait(command + "-final")
				if stall == command+"-error" {
					_, _ = io.WriteString(control, "426 transfer failed\r\n226 data complete\r\n")
				} else {
					_, _ = io.WriteString(control, "226 data complete\r\n")
				}
			case "NOOP":
				wait("NOOP")
				_, _ = io.WriteString(control, "200 alive\r\n")
			case "QUIT":
				return
			default:
				_, _ = io.WriteString(control, "200 ok\r\n")
			}
		}
	}(serverCtx)
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	connector, err := New(transportsecurity.WithPlaintext(setupCtx), Config{Host: host, Port: number, AllowPrivateHosts: true}, Credentials{Username: "synthetic", Password: "synthetic"})
	require.NoError(t, err)
	return connector, reached, release
}
