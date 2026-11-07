package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/fake"
	"github.com/Braidplane/braidplane-core/pkg/kongcmd"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type Options struct {
	Config           string `default:"config.toml" help:"Path to config file."`
	GitOnly          bool   `name:"git-only" help:"Bootstrap only the seeded Git repository fixtures."`
	CoreOnly         bool   `name:"core-only" help:"Seed identities, organizations and projects without connector fixtures."`
	GitFixtureState  string `name:"git-fixture-state" default:"base" enum:"base,deleted,restored" help:"Pipeline lifecycle state for --git-only."`
	ScheduleFireSeed bool   `name:"schedule-fire-seed" help:"Read schedule-fire seed rows from stdin and write them to the configured database."`
}

// Run parses the shared development seeder flags and dispatches the requested seed action.
func Run(args []string, stdout, stderr io.Writer) error {
	configArgs, commandArgs, err := config.ExtractFlagArgs(&backendconfig.Config{}, args)
	if err != nil {
		return xerror.Unexpected().Detailf("parse fake seed config flags: %w", err)
	}

	result, err := kongcmd.Parse(
		commandArgs,
		Options{},
		"fake",
		"Seed local fake data for development workflows.",
		stdout,
		stderr,
		func(code int) error {
			return xerror.ValidationFailed().Detailf("fake seed command exited with status %d", code)
		},
	)
	if result.Exited {
		return err
	}
	if err != nil {
		return xerror.ValidationFailed().Detailf("parse fake seed command: %w", err)
	}

	return Execute(context.Background(), result.CLI, configArgs, stdout)
}

// Execute runs the selected shared seed mode with backend configuration overrides.
func Execute(ctx context.Context, cli Options, configArgs []string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if (cli.GitOnly && cli.ScheduleFireSeed) || (cli.CoreOnly && (cli.GitOnly || cli.ScheduleFireSeed)) {
		return xerror.ValidationFailed().Detail("--core-only, --git-only and --schedule-fire-seed are mutually exclusive")
	}

	if cli.GitOnly {
		repoURL, commitSHA, err := fake.BootstrapSeededGitFixtureState(ctx, cli.GitFixtureState)
		if err != nil {
			return xerror.Wrapf("failed to bootstrap seeded git fixtures: %w", err)
		}

		return writeFakeOutput(stdout, fmt.Sprintf(
			"seeded_git_repo_url=%s\nseeded_git_commit_sha=%s\n",
			repoURL,
			commitSHA,
		))
	}
	if cli.GitFixtureState != "base" {
		return xerror.ValidationFailed().Detail("--git-fixture-state requires --git-only")
	}

	cfg, err := backendconfig.LoadWithArgs(cli.Config, configArgs)
	if err != nil {
		return xerror.Wrapf("failed to load config: %w", err)
	}

	if cli.ScheduleFireSeed {
		return seedScheduleFiresFromReader(ctx, os.Stdin, cfg.Database.ConnectionString())
	}

	summary, err := fake.Seed(ctx, cfg, cli.CoreOnly)
	if err != nil {
		return xerror.Wrapf("failed to seed fake data: %w", err)
	}

	return writeFakeSummary(stdout, summary)
}

// writeFakeSummary writes the seeded slugs and credentials consumed by local scripts.
func writeFakeSummary(stdout io.Writer, summary *fake.Summary) error {
	var output strings.Builder

	fmt.Fprintf(&output, "fake seed completed (run_slug=%s)\n", summary.RunID)
	fmt.Fprintf(&output, "owner_user_slug=%s\n", summary.OwnerUserSlug)
	fmt.Fprintf(&output, "member_user_slug=%s\n", summary.MemberUserSlug)
	fmt.Fprintf(&output, "owner_user_email=%s\n", summary.OwnerUserEmail)
	fmt.Fprintf(&output, "member_user_email=%s\n", summary.MemberUserEmail)
	fmt.Fprintf(&output, "user_password=%s\n", summary.UserPassword)
	fmt.Fprintf(&output, "organization_slug=%s\n", summary.OrganizationSlug)
	fmt.Fprintf(&output, "empty_organization_slug=%s\n", summary.EmptyOrganizationSlug)
	fmt.Fprintf(&output, "project_slug=%s\n", summary.ProjectSlug)
	fmt.Fprintf(&output, "environment_slug=%s\n", summary.EnvironmentSlug)
	fmt.Fprintf(&output, "api_key_slug=%s\n", summary.APIKeySlug)
	fmt.Fprintf(&output, "api_key_token=%s\n", summary.APIKeyToken)
	fmt.Fprintf(&output, "http_data_source_slug=%s\n", summary.HTTPDataSourceSlug)
	fmt.Fprintf(&output, "http_query_slug=%s\n", summary.HTTPQuerySlug)
	fmt.Fprintf(&output, "data_source_slug=%s\n", summary.DataSourceSlug)
	fmt.Fprintf(&output, "query_slug=%s\n", summary.QuerySlug)
	fmt.Fprintf(&output, "sql_data_source_slug=%s\n", summary.SQLDataSourceSlug)
	fmt.Fprintf(&output, "sql_query_slug=%s\n", summary.SQLQuerySlug)
	fmt.Fprintf(&output, "mysql_data_source_slug=%s\n", summary.MySQLDataSourceSlug)
	fmt.Fprintf(&output, "mysql_query_slug=%s\n", summary.MySQLQuerySlug)
	fmt.Fprintf(&output, "mysql_sink_query_slug=%s\n", summary.MySQLSinkQuerySlug)
	fmt.Fprintf(&output, "mongo_data_source_slug=%s\n", summary.MongoDataSourceSlug)
	fmt.Fprintf(&output, "mongo_query_slug=%s\n", summary.MongoQuerySlug)
	fmt.Fprintf(&output, "hubspot_data_source_slug=%s\n", summary.HubSpotDataSourceSlug)
	fmt.Fprintf(&output, "hubspot_query_slug=%s\n", summary.HubSpotQuerySlug)
	fmt.Fprintf(&output, "ftp_data_source_slug=%s\n", summary.FTPDataSourceSlug)
	fmt.Fprintf(&output, "ftp_query_slug=%s\n", summary.FTPQuerySlug)
	fmt.Fprintf(&output, "git_data_source_slug=%s\n", summary.GitDataSourceSlug)
	fmt.Fprintf(&output, "git_query_slug=%s\n", summary.GitQuerySlug)
	fmt.Fprintf(&output, "seeded_git_commit_sha=%s\n", summary.SeededGitCommitSHA)
	fmt.Fprintf(&output, "pipeline_slug=%s\n", summary.PipelineSlug)
	fmt.Fprintf(&output, "notification_record_slug=%s\n", summary.NotificationRecordSlug)
	fmt.Fprintf(&output, "retention_run_slug=%s\n", summary.RetentionRunSlug)

	return writeFakeOutput(stdout, output.String())
}

// writeFakeOutput reports writer failures instead of silently dropping seed output.
func writeFakeOutput(stdout io.Writer, output string) error {
	if _, err := io.WriteString(stdout, output); err != nil {
		return xerror.Unexpected().Detailf("write fake seed output: %w", err)
	}
	return nil
}
