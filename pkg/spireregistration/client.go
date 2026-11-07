// Package spireregistration administers only explicitly owned workload entries.
// It is used by the isolated control plane, never by connector executors.
package spireregistration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

var (
	ErrUnavailable = xerror.Unavailable(xerror.DataSource).Detail("workload registration reconciliation unavailable")
	ErrConflict    = xerror.Unavailable(xerror.DataSource).Detail("workload registration ownership conflict")
)

type Client struct {
	Binary   string
	Socket   string
	Parent   spiffeid.ID
	Selector string
}

type spiffeID struct {
	TrustDomain string `json:"trust_domain"`
	Path        string `json:"path"`
}

type entry struct {
	ID        string   `json:"id"`
	Subject   spiffeID `json:"spiffe_id"`
	Parent    spiffeID `json:"parent_id"`
	Selectors []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"selectors"`
	JWTTTL        int      `json:"jwt_svid_ttl"`
	Admin         bool     `json:"admin"`
	Downstream    bool     `json:"downstream"`
	FederatesWith []string `json:"federates_with"`
	ExpiresAt     string   `json:"expires_at"`
}

// Check verifies the pinned SPIRE server's own dependency health without changing registrations.
func (c Client) Check(ctx context.Context) error {
	_, err := c.run(ctx, "healthcheck")
	return err
}

func (c Client) Ensure(ctx context.Context, id string, subject spiffeid.ID) error {
	current, err := c.find(ctx, id, subject)
	if err != nil {
		return err
	}
	operation := "create"
	if current != nil {
		if c.matches(*current) {
			return nil
		}
		operation = "update"
	}
	_, err = c.run(ctx, operation, "-entryID", id, "-spiffeID", subject.String(),
		"-parentID", c.Parent.String(), "-selector", c.Selector, "-jwtSVIDTTL", "300")
	if err != nil {
		return err
	}
	current, err = c.find(ctx, id, subject)
	if err != nil {
		return err
	}
	if current == nil || !c.matches(*current) {
		return ErrUnavailable
	}
	return nil
}

func (c Client) Revoke(ctx context.Context, id string, subject spiffeid.ID) error {
	current, err := c.find(ctx, id, subject)
	if err != nil || current == nil {
		return err
	}
	if _, err := c.run(ctx, "delete", "-entryID", id); err != nil {
		return err
	}
	current, err = c.find(ctx, id, subject)
	if err != nil {
		return err
	}
	if current != nil {
		return ErrUnavailable
	}
	return nil
}

func (c Client) find(ctx context.Context, id string, subject spiffeid.ID) (*entry, error) {
	if id == "" || subject.IsZero() || c.Parent.IsZero() || subject.TrustDomain() != c.Parent.TrustDomain() {
		return nil, ErrConflict
	}
	data, err := c.run(ctx, "show", "-spiffeID", subject.String())
	if err != nil {
		return nil, err
	}
	var result struct {
		Entries  []entry `json:"entries"`
		NextPage string  `json:"next_page_token"`
	}
	if json.Unmarshal(data, &result) != nil || result.NextPage != "" {
		return nil, ErrUnavailable
	}
	if len(result.Entries) == 0 {
		return nil, nil //nolint:nilnil // Absence is expected during idempotent creation and revocation.
	}
	if len(result.Entries) != 1 {
		return nil, ErrConflict
	}
	found := result.Entries[0]
	if found.ID != id || found.Subject.TrustDomain != subject.TrustDomain().String() || found.Subject.Path != subject.Path() {
		return nil, ErrConflict
	}
	return &found, nil
}

func (c Client) matches(e entry) bool {
	selectorType, selectorValue, ok := strings.Cut(c.Selector, ":")
	return ok && e.Parent.TrustDomain == c.Parent.TrustDomain().String() && e.Parent.Path == c.Parent.Path() &&
		len(e.Selectors) == 1 && e.Selectors[0].Type == selectorType && e.Selectors[0].Value == selectorValue &&
		e.JWTTTL == 300 && !e.Admin && !e.Downstream && len(e.FederatesWith) == 0 && (e.ExpiresAt == "" || e.ExpiresAt == "0")
}

func (c Client) run(ctx context.Context, operation string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	argv := append([]string{"entry", operation, "-socketPath", c.Socket, "-output", "json"}, args...)
	if operation == "healthcheck" {
		argv = []string{"healthcheck", "-socketPath", c.Socket}
	}
	// #nosec G204 -- executable and socket are operator configuration; arguments never use a shell.
	cmd := exec.CommandContext(ctx, c.Binary, argv...)
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, ErrUnavailable
	}
	return output.buffer.Bytes(), nil
}

type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 1<<20 {
		return 0, ErrUnavailable
	}
	_, _ = b.buffer.Write(p)
	return len(p), nil
}
