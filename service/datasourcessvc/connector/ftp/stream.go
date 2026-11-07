package ftp

import (
	"context"
	"io"
	"path"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

func (p ftpProvider) ReadFileTo(ctx context.Context, target string, maxBytes int64, dst io.Writer) (metadata *filestream.Metadata, retErr error) {
	finish, err := p.connector.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := finish(); err != nil {
			metadata, retErr = nil, xerror.ExecutionFailed().With(err)
		}
	}()
	response, err := p.connector.conn.Retr(target)
	if err != nil {
		return nil, xerror.ExecutionFailed().With(err)
	}
	metadata, copyErr := filestream.Copy(ctx, dst, response, maxBytes)
	closeErr := response.Close()
	if closeErr != nil {
		_ = p.connector.closeSockets()
	}
	if err := xerror.Join(copyErr, closeErr); err != nil {
		return nil, xerror.ExecutionFailed().With(err)
	}
	return metadata, nil
}

func (p ftpProvider) WriteFileFrom(ctx context.Context, req remotefile.StreamWriteRequest) (_ int64, retErr error) {
	c := p.connector
	// FTP has no atomic conditional rename; refuse a racy no-overwrite promise.
	if !req.Overwrite {
		return 0, xerror.Unsupported(xerror.DataSource).WithPublicDetail("FTP streaming writes require overwrite; atomic no-overwrite publication is unavailable.")
	}
	finish, err := c.beginOperation(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := finish(); err != nil {
			retErr = xerror.ExecutionFailed().With(xerror.Join(retErr, err))
		}
	}()
	if err := ctx.Err(); err != nil {
		return 0, xerror.ExecutionFailed().With(err)
	}
	verified, err := filestream.NewVerifiedReader(ctx, req.Body, req.Reference.SizeBytes, req.Reference.SHA256)
	if err != nil {
		return 0, err
	}
	temporary := path.Join(path.Dir(req.Target), ".braidplane-stream-"+uuid.New().String()+".part")
	published := false
	defer func() {
		if published {
			return
		}
		// Cancellation invalidates this session. Cleanup uses a fresh bounded session.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		cleanup, err := New(cleanupCtx, c.config, c.credentials)
		if err == nil {
			finishCleanup, beginErr := cleanup.beginOperation(cleanupCtx)
			if beginErr == nil {
				err = cleanup.conn.Delete(temporary)
				err = xerror.Join(err, finishCleanup())
			} else {
				err = beginErr
			}
			closeErr := cleanup.Close()
			err = xerror.Join(err, closeErr)
		}
		if err != nil {
			retErr = xerror.ExecutionFailed().With(xerror.Join(retErr, err)).WithPublicDetail("FTP temporary-file cleanup failed; inspect partial uploads before recovery.")
		}
	}()
	if err := c.conn.Stor(temporary, verified); err != nil {
		return 0, xerror.ExecutionFailed().With(err)
	}
	if err := ctx.Err(); err != nil {
		return 0, xerror.ExecutionFailed().With(err)
	}
	if err := c.conn.Rename(temporary, req.Target); err != nil {
		return 0, xerror.ExecutionFailed().With(err).WithPublicDetail("FTP rename acknowledgement failed; inspect the destination before recovery.")
	}
	published = true
	return req.Reference.SizeBytes, nil
}
