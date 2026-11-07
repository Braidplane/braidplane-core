# API reference

Use the Braidplane API for automation and direct integrations. Most resources
belong to an organization, and pipeline resources also name a project and
environment in the URL.

This page explains the common conventions. The
[OpenAPI specification]({{ public_openapi_url }}) is the complete reference for
paths, request bodies, response schemas, and validation rules.

See the [Alpha API contract](alpha.md) for
version matching, authentication exceptions, upgrades and execution retries.
Use the policy and OpenAPI document from the same release as your application.

## Base URL

Use your installation's HTTPS origin with the `/api` prefix, for example
`https://braidplane.example/api`. Community serves the API on the same origin
as the application. Send tokens only to the installation you intend to use.

## Authentication

Send a personal access token or organization API key as a bearer token:

```http
Authorization: Bearer <token>
```

Personal access tokens act as you and use your current workspace membership and
permissions. Organization API keys are machine identities for automation. Give
either credential only the permissions it needs, and keep it out of source
files, pipeline specifications, logs, and support messages.

Create a personal access token under **Account → Security → Personal access
tokens**. Organization administrators can create API keys with the API or CLI.
The raw value is shown once.

## Make a request

This example fetches one workspace. `read -s` keeps the token out of shell
history and off the screen:

```bash
export BRAIDPLANE_API_URL='https://braidplane.example/api'
export BRAIDPLANE_ORG='<workspace-slug>'
read -r -s -p 'Braidplane token: ' BRAIDPLANE_TOKEN
printf '\n'

curl --fail-with-body --silent --show-error \
  --header "Authorization: Bearer $BRAIDPLANE_TOKEN" \
  "$BRAIDPLANE_API_URL/organizations/$BRAIDPLANE_ORG"

unset BRAIDPLANE_TOKEN
```

A successful response is JSON describing the workspace.
Use external slugs from API responses and URLs; internal database IDs are not
part of the public contract.

## Common resource paths

| Task | Method and path |
|---|---|
| Get a workspace | `GET /organizations/{organization}` |
| List data sources | `GET /organizations/{organization}/data-sources` |
| Test a saved data source | `POST /organizations/{organization}/data-sources/{data_source}/test-connection` |
| List pipelines | `GET /organizations/{organization}/projects/{project}/pipelines` |
| Validate a pipeline | `POST /organizations/{organization}/projects/{project}/environments/{environment}/pipeline-validation` |
| Execute a deployed pipeline | `POST /organizations/{organization}/projects/{project}/environments/{environment}/pipelines/{pipeline}:execute` |
| List runs | `GET /organizations/{organization}/projects/{project}/environments/{environment}/runs` |

The OpenAPI specification lists required bodies, permissions, and every
response for these routes.

## Pagination

Pagination varies by endpoint. Check its query parameters and response schema
in OpenAPI before listing. For endpoints that use `cursor`, start without one;
when the response has `has_more: true`, pass its `next_cursor` for the next page.
Some endpoints use a different token: Google Workspace file discovery returns
`next_page_token`, which is passed back as `page_token`. Other lists, such as
pipeline versions and data-source branches, do not expose pagination parameters.
Use only the filters, sorting options, and continuation fields documented for
the selected endpoint.

## Errors

Errors use a common JSON wrapper:

```json
{
  "error": {
    "code": "ENTITY_NAME_INVALID",
    "message": "Entity name must be between 1 and 100 characters",
    "request_id": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

Use `code` for program logic, show `message` to a person, and keep `request_id`
when contacting support. Validation errors can also include field-level
details. Never expose your bearer token in a support request.

## Pipeline request behavior

- Select project environments explicitly for environment-aware validation,
  deployment, binding, and run operations.
- An accepted execution is asynchronous. Keep the returned run slug and poll
  its run resource until it reaches `succeeded`, `failed`, or `canceled`.
- The `input` object in execution and checkpoint-bootstrap requests can lose
  numeric precision when the API reads it. Use strings for identifiers or exact
  decimal values when the pipeline accepts them. Otherwise, verify the target
  server's numeric behavior before starting a run or bootstrapping checkpoints.
- Handle validation, authentication, authorization, missing-resource, and
  conflict responses according to the status and schema in the OpenAPI
  document.
