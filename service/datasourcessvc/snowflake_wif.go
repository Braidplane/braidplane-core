package datasourcessvc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	repowif "github.com/Braidplane/braidplane-core/postgres/snowflakewif"
	snowflakeconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake"
	"github.com/Braidplane/braidplane-core/service/snowflakewifsvc"
)

func (s *service) syncSnowflakeWIF(ctx context.Context, db postgres.DBTX, org slug.Slug, ds *repods.DataSource, credentials []byte, disabled bool) error {
	if ds.Type != model.DataSourceTypeSnowflake {
		return nil
	}
	var account string
	var enabled bool
	if !disabled {
		var err error
		account, enabled, err = s.snowflakeWIFSettings(ds, credentials)
		if err != nil {
			return err
		}
	}
	if enabled {
		var config snowflakeconnector.Config
		if err := json.Unmarshal(ds.Config, &config); err != nil {
			return ErrInvalidInput.Detail("Invalid Snowflake configuration.")
		}
		if err := config.ValidateOnboarding(); err != nil {
			return err
		}
	}
	_, err := snowflakewifsvc.SetDesired(ctx, db, org, ds.Slug, s.snowflakeWIFIssuer, account, enabled)
	if err != nil {
		return err
	}
	if enabled && ds.IsActive {
		// Any identity/configuration change requires explicit verification before activation.
		inactive := false
		updated, err := s.repo.Update(ctx, db, org, ds.Slug, &repods.UpdateParams{Name: ds.Name, ConfigJSON: ds.Config, IsActive: &inactive, ReadOnly: ds.ReadOnly})
		if err != nil {
			return err
		}
		*ds = *updated
	}
	return nil
}

// Automatic tests wait for provisioning; explicit tests still report readiness failures.
func (s *service) testAfterSave(ctx context.Context, org slug.Slug, ds *repods.DataSource, credentials []byte) (*repods.DataSource, error) {
	_, enabled, err := s.snowflakeWIFSettings(ds, credentials)
	if err != nil {
		return nil, err
	}
	if enabled {
		// The customer must first review and run the generated Snowflake script.
		return ds, nil
	}

	return s.testConnection(ctx, org, ds, credentials)
}

func (s *service) snowflakeWIFSettings(ds *repods.DataSource, credentials []byte) (string, bool, error) {
	if ds.Type != model.DataSourceTypeSnowflake {
		return "", false, nil
	}
	var config struct {
		Account string `json:"account"`
	}
	var auth struct {
		AuthType string `json:"auth_type"`
	}
	if json.Unmarshal(ds.Config, &config) != nil || json.Unmarshal(credentials, &auth) != nil {
		return "", false, ErrInvalidInput.Detail("invalid Snowflake workload identity configuration")
	}
	return config.Account, s.snowflakeWIFIssuer != "" && strings.TrimSpace(auth.AuthType) == snowflakeconnector.AuthTypeWorkloadIdentity, nil
}

// SnowflakeOnboarding contains public claims and customer-admin instructions only.
type SnowflakeOnboarding struct {
	IdentityState string
	Active        bool
	Issuer        string
	Subject       string
	Audience      string
	SQL           string
	Diagnostic    string
}

func (s *service) GetSnowflakeOnboarding(ctx context.Context, org, dsSlug slug.Slug) (*SnowflakeOnboarding, error) {
	ds, credentials, err := s.snapshotDataSourceAndCredentials(ctx, org, dsSlug)
	if err != nil {
		return nil, err
	}
	account, enabled, err := s.snowflakeWIFSettings(ds, credentials)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrDataSourceNotFound.Detail("Customer Snowflake workload identity onboarding is not available for this connection.")
	}
	var config snowflakeconnector.Config
	if err := json.Unmarshal(ds.Config, &config); err != nil {
		return nil, ErrInvalidInput.Detail("Invalid Snowflake configuration.")
	}
	identity, err := snowflakewif.NewIdentity(s.snowflakeWIFIssuer, org, dsSlug, account)
	if err != nil {
		return nil, err
	}
	script, err := (snowflakewif.Setup{Warehouse: config.Warehouse, Database: config.Database, Schema: config.Schema, Role: config.Role, Objects: config.WIFObjects}).Script(identity)
	if err != nil {
		return nil, err
	}
	state, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (string, error) {
		return (repowif.Repository{}).OnboardingState(ctx, db, org, dsSlug, s.snowflakeWIFIssuer, account)
	})
	if err != nil {
		return nil, xerror.Wrapf("read Snowflake onboarding state: %w", err)
	}
	diagnostic := "Braidplane is provisioning this identity. Retry shortly."
	switch state {
	case "ready":
		diagnostic = "Run the SQL script in Snowflake, then select Verify."
	case "failed":
		diagnostic = "Braidplane identity provisioning failed and will retry automatically. Retry shortly."
	case "revoking":
		diagnostic = "Identity revocation is in progress. Existing Snowflake sessions may require termination for immediate removal."
	case "disabled":
		diagnostic = "Identity is disabled. Enable the connection to request provisioning again. Existing Snowflake sessions may require termination for immediate removal."
	}
	active := state == "ready" && ds.IsActive
	if active {
		diagnostic = "Identity and Snowflake verification passed."
	} else if state == "ready" && ds.TestError != nil {
		diagnostic = *ds.TestError
	}
	return &SnowflakeOnboarding{IdentityState: state, Active: active, Issuer: identity.Issuer, Subject: identity.Subject.String(), Audience: identity.Audience, SQL: script, Diagnostic: diagnostic}, nil
}
