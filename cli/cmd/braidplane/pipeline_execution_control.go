package main

import (
	"context"
	"net/http"

	api "github.com/Braidplane/braidplane-core/cli/apiclient"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type pipelineExecutionControlCommand struct {
	Status pipelineExecutionStatusCommand `cmd:"" help:"Inspect held and active attempts and sink effect evidence."`
	Pause  pausePipelineExecutionCommand  `cmd:"" help:"Hold new execution for this pipeline and environment."`
	Resume resumePipelineExecutionCommand `cmd:"" help:"Explicitly resume execution for this pipeline and environment."`
}

type pipelineExecutionStatusCommand struct {
	apiPipelineFlags
	Pipeline    string `arg:"" name:"pipeline-slug" required:""`
	Environment string `name:"environment" env:"BRAIDPLANE_ENVIRONMENT" help:"Project environment slug."`
}

func (c *pipelineExecutionStatusCommand) client(runtime *cliRuntime) (*cliAPIClient, string, string, error) {
	if _, err := slug.New(c.Pipeline); err != nil {
		return nil, "", "", err
	}
	return newPipelineEnvironmentAPIClient(c.apiPipelineFlags, c.Environment, runtime)
}

func (c *pipelineExecutionStatusCommand) Run(output *cliOutput, runtime *cliRuntime) error {
	client, project, environment, err := c.client(runtime)
	if err != nil {
		return err
	}
	response, err := client.openapi.GetPipelineExecutionControlWithResponse(context.Background(), client.organization, project, environment, c.Pipeline)
	if err != nil {
		return err
	}
	return writePipelineExecutionControl(output, response.StatusCode(), response.Body, response.JSON200)
}

type setPipelineExecutionCommand struct {
	pipelineExecutionStatusCommand
	ExpectedRevision int64 `name:"expected-revision" required:"" help:"Revision from status. Reload after a conflict."`
}

func (c *setPipelineExecutionCommand) set(output *cliOutput, runtime *cliRuntime, paused bool) error {
	if c.ExpectedRevision < 0 {
		return xerror.ValidationFailed().Detail("expected revision must be nonnegative")
	}
	client, project, environment, err := c.client(runtime)
	if err != nil {
		return err
	}
	response, err := client.openapi.SetPipelineExecutionControlWithResponse(context.Background(), client.organization, project, environment, c.Pipeline, api.PipelineExecutionControlUpdate{ExpectedRevision: c.ExpectedRevision, Paused: paused})
	if err != nil {
		return err
	}
	return writePipelineExecutionControl(output, response.StatusCode(), response.Body, response.JSON200)
}

type pausePipelineExecutionCommand struct{ setPipelineExecutionCommand }

func (c *pausePipelineExecutionCommand) Run(output *cliOutput, runtime *cliRuntime) error {
	return c.set(output, runtime, true)
}

type resumePipelineExecutionCommand struct{ setPipelineExecutionCommand }

func (c *resumePipelineExecutionCommand) Run(output *cliOutput, runtime *cliRuntime) error {
	return c.set(output, runtime, false)
}

func writePipelineExecutionControl(output *cliOutput, status int, body []byte, state *api.PipelineExecutionControl) error {
	if status != http.StatusOK {
		return decodeAPIError(status, body)
	}
	if state == nil {
		return xerror.Unexpected().Detail("missing pipeline execution state")
	}
	return writeCLIJSON(output.stdout, state)
}
