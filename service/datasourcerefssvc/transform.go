package datasourcerefssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
)

func toModelRef(ref *datasourcerefs.DataSourceRef) *model.DataSourceRef {
	if ref == nil {
		return nil
	}
	return &model.DataSourceRef{
		PipelineSlug:         ref.PipelineSlug,
		Slug:                 ref.Slug,
		Name:                 ref.Name,
		DataSourceType:       ref.DataSourceType,
		RequiredCapabilities: append([]model.DataSourceRefCapability(nil), ref.RequiredCapabilities...),
		Timestamps: model.Timestamps{
			CreatedAt: ref.CreatedAt,
			UpdatedAt: ref.UpdatedAt,
		},
	}
}

func toModelBinding(binding *datasourcerefs.DataSourceBinding) *model.DataSourceBinding {
	if binding == nil {
		return nil
	}
	return &model.DataSourceBinding{
		PipelineSlug:         binding.PipelineSlug,
		DataSourceRefSlug:    binding.DataSourceRefSlug,
		DataSourceRefName:    binding.DataSourceRefName,
		EnvironmentSlug:      binding.EnvironmentSlug,
		DataSourceSlug:       binding.DataSourceSlug,
		DataSourceName:       binding.DataSourceName,
		DataSourceType:       binding.DataSourceType,
		DataSourceActive:     binding.DataSourceActive,
		DataSourceReadOnly:   binding.DataSourceReadOnly,
		RequiredCapabilities: append([]model.DataSourceRefCapability(nil), binding.RequiredCapabilities...),
		BoundAt:              binding.CreatedAt,
		UpdatedAt:            binding.UpdatedAt,
	}
}
