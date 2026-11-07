package mongo

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestBSONBudgetMatchesDecodedJSON(t *testing.T) {
	t.Parallel()
	for _, value := range []any{
		nil, true, int32(0), int64(9007199254740993), 1.25,
		"<\u2028", strings.Repeat("x", 1024), bson.NewObjectID(),
		bson.NewDateTimeFromTime(time.Unix(0, 0)),
		bson.Timestamp{T: 123, I: 1},
		bson.Binary{Subtype: 0, Data: []byte{1, 2, 3}},
		bson.Binary{Subtype: 0, Data: []byte{}},
		bson.Binary{Subtype: 2, Data: []byte{}},
		bson.Regex{Pattern: "<value>", Options: "i"},
		bson.DBPointer{DB: "db", Pointer: bson.NewObjectID()},
		bson.JavaScript("<code>"), bson.Symbol("symbol"),
		bson.Undefined{},
		bson.MinKey{},
		bson.MaxKey{},
		bson.NewDecimal128(1, 2),
		bson.M{"nested": bson.A{int32(0), "<", bson.M{"key": true}}},
		bson.CodeWithScope{Code: "code", Scope: bson.M{"key": bson.A{1, 2}}},
	} {
		raw, err := bson.Marshal(bson.M{"value": value})
		require.NoError(t, err)
		var decoded bson.M
		require.NoError(t, bson.Unmarshal(raw, &decoded))
		encoded, err := json.Marshal(decoded)
		require.NoError(t, err)
		require.NoError(t, chargeBSONDocument(result.NewResponseBudget(int64(len(encoded))), raw, false, 0), "%T %s", value, encoded)
		require.ErrorIs(t, chargeBSONDocument(result.NewResponseBudget(int64(len(encoded)-1)), raw, false, 0), xerror.ExecutionResponseSizeExceeded(), "%T %s", value, encoded)
	}
}

func TestBSONBudgetAcceptsBSONOverhead(t *testing.T) {
	t.Parallel()
	doc := bson.M{}
	for i := range 1000 {
		doc[fmt.Sprint(i)] = int32(0)
	}
	raw, err := bson.Marshal(doc)
	require.NoError(t, err)
	encoded, err := json.Marshal(doc)
	require.NoError(t, err)
	require.Greater(t, len(raw), len(encoded))
	require.NoError(t, chargeBSONDocument(result.NewResponseBudget(int64(len(encoded))), raw, false, 0))
}

func TestBSONBudgetRejectsMalformedAndDeepDocuments(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{{}, {4, 0, 0, 0}, {5, 0, 0, 0, 1}, {6, 0, 0, 0, 0xff, 0}} {
		err := chargeBSONDocument(result.NewResponseBudget(1024), raw, false, 0)
		require.ErrorIs(t, err, xerror.Unexpected())
	}
	raw := bsoncore.BuildDocument(nil)
	for range 1002 {
		raw = bsoncore.BuildDocument(nil, bsoncore.AppendDocumentElement(nil, "nested", raw))
	}
	err := chargeBSONDocument(result.NewResponseBudget(1<<20), raw, false, 0)
	require.ErrorIs(t, err, xerror.Unexpected())
	require.ErrorContains(t, err, "nesting")
}
