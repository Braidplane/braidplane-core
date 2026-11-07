package ftp

import (
	"context"
	"net"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// The FTP client is not concurrent-safe; waiting callers must still honor cancellation.
func (c *Connector) beginOperation(ctx context.Context) (func() error, error) {
	if c == nil || c.conn == nil {
		return nil, xerror.Unexpected().Detail("FTP connector is not initialized")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c.operation <- struct{}{}:
	}
	c.mu.Lock()
	if c.closed || ctx.Err() != nil {
		c.mu.Unlock()
		<-c.operation
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, xerror.Unexpected().Detail("FTP connector is closed")
	}
	c.operationCtx = ctx
	c.mu.Unlock()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.closeSockets()
		close(done)
	})
	return func() error {
		// A started callback must finish before another operation can acquire the session.
		if !stop() {
			<-done
		}
		err := ctx.Err()
		if err != nil {
			_ = c.closeSockets()
		}
		c.mu.Lock()
		c.operationCtx, c.data = nil, nil
		c.mu.Unlock()
		<-c.operation
		return err
	}, nil
}

func (c *Connector) dialData(policy hostguard.DestinationPolicy, dialer *net.Dialer, network, address string) (net.Conn, error) {
	c.mu.Lock()
	ctx, closed := c.operationCtx, c.closed
	c.mu.Unlock()
	if closed || ctx == nil {
		return nil, net.ErrClosed
	}
	socket, err := ftpDialFunc(ctx, policy, dialer)(network, address)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		_ = socket.Close()
		return nil, err
	}
	if c.closed {
		_ = socket.Close()
		return nil, net.ErrClosed
	}
	c.data = socket
	return socket, nil
}

// Close raw sockets so cancellation also interrupts TLS and final control replies.
func (c *Connector) closeSockets() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.data != nil {
		_ = c.data.Close()
	}
	if c.control != nil {
		if err := c.control.Close(); err != nil {
			return xerror.Wrapf("close FTP control connection: %w", err)
		}
	}
	return nil
}
