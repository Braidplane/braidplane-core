package ftp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
)

const MaxArtifactBytes = 10 << 20

var ErrArtifactChanged = xerror.New(xerror.KindInvalidInput, "FTP_ARTIFACT_CHANGED", "FTP artifact bytes differ from receipt")

var artifactSegment = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

type ArtifactEvidence struct {
	DeliveryID  uuid.UUID
	FinalPath   string
	StagingPath string
	SizeBytes   int64
	SHA256      string
}

// PrepareArtifactCSV performs no provider I/O. The directory must come from a
// qualified storage binding and the filename from the pinned write operation.
func PrepareArtifactCSV(config Config, directory, filename string, id uuid.UUID, limit int64, content any, options filedata.ParseCSVOptions) (ArtifactEvidence, []byte, error) {
	stem, csv := strings.CutSuffix(filename, ".csv")
	if id == uuid.Nil || !csv || !artifactSegment.MatchString(stem) || limit <= 0 {
		return ArtifactEvidence{}, nil, filestream.ErrUnavailable
	}
	for _, segment := range strings.Split(directory, "/") {
		if !artifactSegment.MatchString(segment) {
			return ArtifactEvidence{}, nil, filestream.ErrUnavailable
		}
	}
	dir, err := joinFTPPath(config.BasePath, directory)
	if err != nil {
		return ArtifactEvidence{}, nil, err
	}
	options.MaxBytes = min(limit, MaxArtifactBytes)
	body, _, err := filedata.EncodeCSVContent(content, options)
	if err != nil {
		return ArtifactEvidence{}, nil, err
	}
	digest := sha256.Sum256(body)
	return ArtifactEvidence{
		DeliveryID: id, FinalPath: path.Join(dir, stem+"-"+id.String()+".csv"),
		StagingPath: path.Join(dir, "braidplane-staging-"+id.String()+".part"),
		SizeBytes:   int64(len(body)), SHA256: hex.EncodeToString(digest[:]),
	}, body, nil
}

// PublishArtifact receives evidence and bytes already matched by the trusted
// executor. beforeRename rechecks the live database claim after STOR.
func (c *Connector) PublishArtifact(ctx context.Context, evidence ArtifactEvidence, body io.Reader, beforeRename func(context.Context) error) (retErr error) {
	if evidence.DeliveryID == uuid.Nil || path.Dir(evidence.FinalPath) != path.Dir(evidence.StagingPath) ||
		!strings.HasSuffix(path.Base(evidence.FinalPath), "-"+evidence.DeliveryID.String()+".csv") ||
		path.Base(evidence.StagingPath) != "braidplane-staging-"+evidence.DeliveryID.String()+".part" ||
		!c.validArtifactTarget(evidence.FinalPath) || !validFTPPathText(evidence.StagingPath) ||
		evidence.SizeBytes > MaxArtifactBytes || beforeRename == nil {
		return filestream.ErrUnavailable
	}
	verified, err := filestream.NewVerifiedReader(ctx, body, evidence.SizeBytes, evidence.SHA256)
	if err != nil {
		return err
	}
	finish, err := c.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = xerror.Join(retErr, finish()) }()
	// A successful directory listing distinguishes existing names from a
	// refusal to inspect the namespace. Exclusivity is a site prerequisite.
	names, err := c.conn.NameList(path.Dir(evidence.FinalPath))
	if err != nil {
		return xerror.ExecutionFailed().With(err)
	}
	for _, name := range names {
		if path.Base(name) == path.Base(evidence.FinalPath) || path.Base(name) == path.Base(evidence.StagingPath) {
			return filestream.ErrUnavailable
		}
	}
	if err := c.conn.Stor(evidence.StagingPath, verified); err != nil {
		return xerror.ExecutionFailed().With(err)
	}
	if !verified.Complete() {
		return filestream.ErrUnavailable
	}
	if err := beforeRename(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.conn.Rename(evidence.StagingPath, evidence.FinalPath); err != nil {
		return xerror.ExecutionFailed().With(err)
	}
	return nil
}

// FetchArtifactCSV fetches one copy and checks its identity before parsing.
func (c *Connector) FetchArtifactCSV(ctx context.Context, evidence ArtifactEvidence, limit int64, options filedata.ParseCSVOptions) (*result.FileResult, error) {
	maxBytes := min(limit, MaxArtifactBytes)
	if maxBytes <= 0 || evidence.SizeBytes < 0 || evidence.SizeBytes > maxBytes ||
		evidence.DeliveryID == uuid.Nil || !c.validArtifactTarget(evidence.FinalPath) ||
		!strings.HasSuffix(path.Base(evidence.FinalPath), "-"+evidence.DeliveryID.String()+".csv") {
		return nil, filestream.ErrUnavailable
	}
	var body bytes.Buffer
	metadata, err := (ftpProvider{connector: c}).ReadFileTo(ctx, evidence.FinalPath, min(maxBytes, evidence.SizeBytes+1), &body)
	if err != nil {
		return nil, err
	}
	if metadata.SizeBytes != evidence.SizeBytes || metadata.SHA256 != evidence.SHA256 {
		return nil, ErrArtifactChanged
	}
	options.MaxBytes = maxBytes
	return filedata.ParseCSV(body.Bytes(), options)
}

func (c *Connector) validArtifactTarget(target string) bool {
	if !strings.HasPrefix(target, "/") || path.Clean(target) != target || !validFTPPathText(target) {
		return false
	}
	base := path.Clean("/" + strings.TrimSpace(c.basePath))
	directory := path.Dir(target)
	return directory != base && (base == "/" || strings.HasPrefix(directory, base+"/"))
}
