package queriessvc

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repoq "github.com/Braidplane/braidplane-core/postgres/queries"
)

// toQuery converts a repository Query and its parameters to the service Query type.
func toQuery(repoQ *repoq.Query, params []*repoq.QueryParameter) (*Query, error) {
	qps := make([]*QueryParameter, 0, len(params))
	for _, p := range params {
		qps = append(qps, toQueryParameter(p))
	}

	expectedInputContract, _, err := decodeSchemaContract(repoQ.ExpectedInputContract)
	if err != nil {
		return nil, xerror.Wrapf(
			"decode expected input contract for query %q: %w",
			repoQ.Slug,
			err,
		)
	}

	outputContract, _, err := decodeSchemaContract(repoQ.OutputContract)
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("decode saved output contract")
	}
	inputRef, _, err := decodeContractRef(repoQ.ExpectedInputContractRef)
	if err != nil {
		return nil, err
	}
	outputRef, _, err := decodeContractRef(repoQ.OutputContractRef)
	if err != nil {
		return nil, err
	}
	return &Query{
		Slug:                     repoQ.Slug,
		Name:                     repoQ.Name,
		Description:              repoQ.Description,
		DataSourceSlug:           repoQ.DataSourceSlug,
		Data:                     repoQ.Data,
		ExpectedInputContract:    expectedInputContract,
		ExpectedInputContractRef: inputRef,
		OutputContract:           outputContract,
		OutputContractRef:        outputRef,
		Parameters:               qps,
		CreatedAt:                repoQ.CreatedAt,
		UpdatedAt:                repoQ.UpdatedAt,
		DeletedAt:                repoQ.DeletedAt,
	}, nil
}

// toQueryParameter converts a repository QueryParameter to the service QueryParameter type.
func toQueryParameter(rp *repoq.QueryParameter) *QueryParameter {
	return &QueryParameter{
		Slug:         rp.Slug,
		Name:         rp.Name,
		DataType:     ParameterDataTypeString(rp.DataType),
		IsRequired:   rp.IsRequired,
		DefaultValue: rp.DefaultValue,
		Description:  rp.Description,
	}
}

// toExecution converts a repository QueryExecution to the service QueryExecution type.
func toExecution(repoExec *repoq.QueryExecution) *QueryExecution {
	return &QueryExecution{
		Slug:       repoExec.Slug,
		Status:     repoExec.Status,
		Result:     repoExec.Result,
		ExecutedAt: repoExec.ExecutedAt,
	}
}

// toRepoCreateParams converts service CreateParams to repository CreateParams.
func toRepoCreateParams(dsSlug slug.Slug, p *CreateParams) (*repoq.CreateParams, error) {
	paramParams := make([]*repoq.QueryParameterParams, 0, len(p.Parameters))
	for _, qp := range p.Parameters {
		paramParam, err := toRepoParamParams(qp, p.Slug)
		if err != nil {
			return nil, err
		}
		paramParams = append(paramParams, paramParam)
	}

	expectedInputContract, err := encodeSchemaContract(p.ExpectedInputContract)
	if err != nil {
		return nil, err
	}

	outputContract, err := encodeSchemaContract(p.OutputContract)
	if err != nil {
		return nil, err
	}
	inputRef, err := encodeContractRef(p.ExpectedInputContractRef)
	if err != nil {
		return nil, err
	}
	outputRef, err := encodeContractRef(p.OutputContractRef)
	if err != nil {
		return nil, err
	}
	return &repoq.CreateParams{
		Slug:                     p.Slug,
		Name:                     p.Name,
		Description:              p.Description,
		DataSourceSlug:           dsSlug,
		Data:                     p.Data,
		ExpectedInputContract:    expectedInputContract,
		ExpectedInputContractRef: inputRef,
		OutputContract:           outputContract,
		OutputContractRef:        outputRef,
		Parameters:               paramParams,
	}, nil
}

// toRepoUpdateParams converts service UpdateParams to repository UpdateParams.
func toRepoUpdateParams(p *UpdateParams, querySlug slug.Slug) (*repoq.UpdateParams, error) {
	paramParams := make([]*repoq.QueryParameterParams, 0, len(p.Parameters))
	for _, qp := range p.Parameters {
		paramParam, err := toRepoParamParams(qp, querySlug)
		if err != nil {
			return nil, err
		}
		paramParams = append(paramParams, paramParam)
	}

	expectedInputContract, err := encodeSchemaContract(p.ExpectedInputContract)
	if err != nil {
		return nil, err
	}

	outputContract, err := encodeSchemaContract(p.OutputContract)
	if err != nil {
		return nil, err
	}
	inputRef, err := encodeContractRef(p.ExpectedInputContractRef)
	if err != nil {
		return nil, err
	}
	outputRef, err := encodeContractRef(p.OutputContractRef)
	if err != nil {
		return nil, err
	}
	return &repoq.UpdateParams{
		Name:                     p.Name,
		Description:              p.Description,
		Data:                     p.Data,
		ExpectedInputContract:    expectedInputContract,
		ExpectedInputContractRef: inputRef,
		OutputContract:           outputContract,
		OutputContractRef:        outputRef,
		Parameters:               paramParams,
	}, nil
}

func decodeSchemaContract(raw json.RawMessage) (*payload.Contract, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}

	var contract payload.Contract
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, false, err
	}

	return &contract, true, nil
}

func encodeSchemaContract(contract *payload.Contract) (json.RawMessage, error) {
	if contract == nil {
		return nil, nil
	}

	data, err := json.Marshal(contract)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// toRepoParamParams converts a service QueryParameter to repository QueryParameterParams.
// The querySlug is used to derive a stable parameter slug when the parameter has no slug set.
func toRepoParamParams(qp *QueryParameter, _ slug.Slug) (*repoq.QueryParameterParams, error) {
	paramSlug, err := qp.Slug.ValidateOrGenerate(qp.Name)
	if err != nil {
		return nil, err
	}

	return &repoq.QueryParameterParams{
		Slug:         paramSlug,
		Name:         qp.Name,
		DataType:     qp.DataType.String(),
		IsRequired:   qp.IsRequired,
		DefaultValue: qp.DefaultValue,
		Description:  qp.Description,
	}, nil
}

func decodeContractRef(raw json.RawMessage) (*pipelinespec.ODCSContractRef, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var ref *pipelinespec.ODCSContractRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, false, xerror.Unexpected().With(err).Detail("decode saved contract reference")
	}
	return ref, true, nil
}

func encodeContractRef(ref *pipelinespec.ODCSContractRef) (json.RawMessage, error) {
	if ref == nil {
		return nil, nil
	}
	return json.Marshal(ref)
}
