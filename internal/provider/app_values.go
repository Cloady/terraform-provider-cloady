package provider

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Cloady/terraform-provider-cloady/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func (r *appResource) listValues(ctx context.Context, data appResourceModel) (map[string]variableRow, error) {
	var result struct {
		Vars []variableRow `json:"vars"`
	}
	if err := r.client.Do(ctx, http.MethodGet, data.apiPath("/vars"), nil, &result); err != nil {
		return nil, err
	}
	rows := make(map[string]variableRow, len(result.Vars))
	for _, row := range result.Vars {
		rows[row.Key] = row
	}
	return rows, nil
}

func (r *appResource) value(ctx context.Context, data appResourceModel, row variableRow) (string, error) {
	if !row.IsSecret {
		return row.Value, nil
	}
	var result struct {
		Value string `json:"value"`
	}
	err := r.client.Do(ctx, http.MethodGet, data.apiPath("/vars/"+url.PathEscape(row.ID)+"/reveal"), nil, &result)
	return result.Value, err
}

func (r *appResource) readValues(ctx context.Context, data *appResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	values := map[string]attr.Value{}
	if len(data.Values.Elements()) > 0 {
		rows, err := r.listValues(ctx, *data)
		if err != nil {
			diags.AddError("Unable to read application values", err.Error())
			return diags
		}
		for key := range data.Values.Elements() {
			if row, exists := rows[key]; exists {
				value, err := r.value(ctx, *data, row)
				if err != nil {
					diags.AddError("Unable to read application value", err.Error())
					return diags
				}
				values[key] = types.StringValue(value)
			}
		}
	}
	data.Values = types.MapValueMust(types.StringType, values)
	return diags
}

func (r *appResource) syncValues(ctx context.Context, data, previous appResourceModel) error {
	desiredValues := data.Values.Elements()
	if len(desiredValues) == 0 && len(previous.Values.Elements()) == 0 {
		return nil
	}
	rows, err := r.listValues(ctx, previous)
	if err != nil {
		return err
	}
	for key, value := range desiredValues {
		desired := value.(types.String).ValueString()
		row, exists := rows[key]
		if !exists {
			// New keys are encrypted; existing keys retain their secret flag.
			if err := r.client.Do(ctx, http.MethodPost, previous.apiPath("/vars"), map[string]any{"key": key, "value": desired, "isSecret": true}, nil); err != nil {
				return err
			}
			continue
		}
		actual, err := r.value(ctx, previous, row)
		if err != nil {
			return err
		}
		if actual != desired {
			if err := r.client.Do(ctx, http.MethodPatch, previous.apiPath("/vars/"+url.PathEscape(row.ID)), map[string]any{"value": desired}, nil); err != nil {
				return err
			}
		}
	}
	for key := range previous.Values.Elements() {
		if _, keep := desiredValues[key]; keep {
			continue
		}
		if row, exists := rows[key]; exists {
			if err := r.client.Do(ctx, http.MethodDelete, previous.apiPath("/vars/"+url.PathEscape(row.ID)), nil, nil); err != nil && !client.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
