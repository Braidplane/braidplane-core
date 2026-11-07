package main

import (
	"context"
	"net/http"

	api "github.com/Braidplane/braidplane-core/cli/apiclient"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type recoverPipelineRunCommand struct {
	apiPipelineFlags
	RunSlug     string `arg:"" name:"run-slug" required:"" help:"Original failed run slug; recovery retains its inputs and effect identity."`
	Environment string `name:"environment" env:"BRAIDPLANE_ENVIRONMENT" help:"Original project environment slug."`
	Confirm     string `name:"confirm-environment" help:"Exact environment slug required for protected environments."`
	cliOutputOptions
}

func (c *recoverPipelineRunCommand) Run(output *cliOutput, runtime *cliRuntime) error {
	if _, err := slug.New(c.RunSlug); err != nil {
		return err
	}
	client, project, environment, err := newPipelineEnvironmentAPIClient(c.apiPipelineFlags, c.Environment, runtime)
	if err != nil {
		return err
	}
	run, err := client.recoverPipelineRun(context.Background(), project, environment, c.RunSlug, api.RecoverPipelineRunRequest{
		ProtectedEnvironmentConfirmation: optionalAPISlug(c.Confirm),
	})
	if err != nil {
		return xerror.Wrapf("recover pipeline run %s: %w", c.RunSlug, err)
	}
	if err := writePipelineRunOutput(output.stdout, run, c.Output); err != nil {
		return err
	}
	if run.Metadata.Status != api.PipelineRunMetadataStatusSucceeded {
		return xerror.ValidationFailed().Detailf("recovery run %s for %s ended with status %s; inspect the returned run diagnostics", run.Metadata.RunSlug, c.RunSlug, run.Metadata.Status)
	}
	return nil
}

func (c *cliAPIClient) recoverPipelineRun(ctx context.Context, project, environment, run string, body api.RecoverPipelineRunRequest) (*api.PipelineRunRecord, error) {
	response, err := c.openapi.RecoverPipelineRunWithResponse(ctx, c.organization, project, environment, run, body)
	if err != nil {
		return nil, xerror.Wrapf("recover pipeline run: %w", err)
	}
	if response.StatusCode() != http.StatusOK {
		return nil, decodeAPIError(response.StatusCode(), response.Body)
	}
	if response.JSON200 == nil || response.JSON200.Metadata.RunSlug == "" {
		return nil, xerror.Unexpected().Detail("unexpected API response: missing recovery run payload")
	}
	return response.JSON200, nil
}
