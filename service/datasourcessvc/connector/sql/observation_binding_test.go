package sql

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/stretchr/testify/require"
)

func TestMetadataBindsOnlyDeclaredStaticRelationAndRole(t *testing.T) {
	resource := TableMetadataResource{Database: "analytics", Schema: "public", Table: "orders"}
	for _, scenario := range []struct {
		statement       string
		source, allowed bool
	}{
		{`SELECT id FROM public.orders`, true, true},
		{`SELECT id FROM public.other`, true, false},
		{`SELECT 'public.orders'`, true, false},
		{`SELECT id FROM public.other /* public.orders */`, true, false},
		{`SELECT id FROM "public"."orders"`, true, true},
		{`SELECT id FROM orders`, true, false},
		{`INSERT INTO public.orders (id) VALUES (1)`, false, true},
		{`INSERT INTO public.other (id) SELECT id FROM public.orders`, false, false},
		{`UPDATE public.other SET id = (SELECT id FROM public.orders LIMIT 1)`, false, false},
		{`DELETE FROM public.other WHERE id IN (SELECT id FROM public.orders)`, false, false},
		{`UPDATE public.orders SET id = 1`, false, true},
		{`DELETE FROM public.orders WHERE id = 1`, false, true},
		{`INSERT INTO public.orders (id) VALUES (1)`, true, false},
		{`SELECT id FROM public.orders; SELECT 1`, true, false},
		{`SELECT id FROM {{table}}`, true, false},
	} {
		raw, err := json.Marshal(map[string]string{"type": "postgres", "sql": scenario.statement})
		require.NoError(t, err)
		require.Equal(t, scenario.allowed, MetadataStatementMatches(model.DataSourceTypePostgres, resource, raw, scenario.source), scenario.statement)
	}
	resource.Schema = resource.Database
	require.True(t, MetadataStatementMatches(model.DataSourceTypeMySQL, resource, json.RawMessage(`{"type":"mysql","sql":"SELECT * FROM orders"}`), true))
	require.False(t, MetadataStatementMatches(model.DataSourceTypeMySQL, resource, json.RawMessage(`{"type":"mysql","sql":"WITH orders AS (SELECT 1 AS id) SELECT * FROM orders"}`), true))
	require.True(t, MetadataStatementMatches(model.DataSourceTypeMySQL, resource, json.RawMessage(`{"type":"mysql","sql":"WITH selected AS (SELECT * FROM analytics.orders) SELECT * FROM selected"}`), true))
	require.False(t, MetadataStatementMatches(model.DataSourceTypeMySQL, resource, json.RawMessage(`{"type":"mysql","sql":"UPDATE analytics.orders JOIN analytics.other ON orders.id=other.id SET other.id=1"}`), false))
}
