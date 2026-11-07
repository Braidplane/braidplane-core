package mongo

import (
	"encoding/base64"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Charge the JSON representation from driver-owned BSON before Decode builds
// the document graph. Variable-width scalars are checked before copying them.
func chargeBSONDocument(budget *result.ResponseBudget, raw []byte, array bool, depth int) error {
	if depth > 1000 {
		return xerror.Unexpected().Detail("MongoDB document nesting exceeds 1000 levels")
	}
	doc, _, ok := bsoncore.ReadDocument(raw)
	if !ok || len(doc) < 5 || doc[len(doc)-1] != 0 {
		return xerror.Unexpected().Detail("invalid BSON document")
	}
	if err := budget.Charge(2); err != nil {
		return err
	}
	remaining := doc[4 : len(doc)-1]
	first := true
	for len(remaining) > 0 {
		element, rest, ok := bsoncore.ReadElement(remaining)
		if !ok {
			return xerror.Unexpected().Detail("invalid BSON element")
		}
		remaining = rest
		if !first {
			if err := budget.Charge(1); err != nil {
				return err
			}
		}
		first = false
		if !array {
			key := element.KeyBytes()
			if err := budget.Check(int64(len(key))); err != nil {
				return err
			}
			if err := budget.ChargeJSON(string(key)); err != nil {
				return err
			}
			if err := budget.Charge(1); err != nil {
				return err
			}
		}
		value := element.Value()
		if err := chargeBSONValue(budget, bson.RawValue{Type: bson.Type(value.Type), Value: value.Data}, depth); err != nil {
			return err
		}
	}
	return nil
}

func chargeBSONValue(budget *result.ResponseBudget, raw bson.RawValue, depth int) error {
	switch raw.Type {
	case bson.TypeEmbeddedDocument, bson.TypeArray:
		return chargeBSONDocument(budget, raw.Value, raw.Type == bson.TypeArray, depth+1)
	case bson.TypeBinary:
		subtype, data, ok := raw.BinaryOK()
		if !ok {
			return xerror.Unexpected().Detail("invalid BSON binary value")
		}
		// Empty Data encodes as ""; add base64 length without copying the bytes.
		if err := budget.ChargeJSON(bson.Binary{Subtype: subtype, Data: []byte{}}); err != nil {
			return err
		}
		length := len(data)
		// The driver retains the four-byte legacy prefix for empty subtype 2.
		if subtype == bson.TypeBinaryBinaryOld && length == 0 {
			length = 4
		}
		return budget.Charge(int64(base64.StdEncoding.EncodedLen(length)))
	case bson.TypeCodeWithScope:
		if len(raw.Value) < 8 {
			return xerror.Unexpected().Detail("invalid BSON code with scope")
		}
		codeLength, _, ok := bsoncore.ReadLength(raw.Value[4:])
		if !ok {
			return xerror.Unexpected().Detail("invalid BSON code length")
		}
		if err := budget.Check(int64(codeLength) - 1); err != nil {
			return err
		}
		code, scope, ok := raw.CodeWithScopeOK()
		if !ok {
			return xerror.Unexpected().Detail("invalid BSON code with scope")
		}
		if err := budget.Charge(int64(len(`{"Code":,"Scope":}`))); err != nil {
			return err
		}
		if err := budget.ChargeJSON(code); err != nil {
			return err
		}
		return chargeBSONDocument(budget, scope, false, depth+1)
	case bson.TypeString, bson.TypeJavaScript, bson.TypeSymbol:
		// BSON strings have a four-byte length and a trailing NUL.
		if err := budget.Check(int64(len(raw.Value) - 5)); err != nil {
			return err
		}
	case bson.TypeRegex, bson.TypeDBPointer:
		if err := budget.Check(int64(len(raw.Value))); err != nil {
			return err
		}
	case bson.TypeDouble, bson.TypeUndefined, bson.TypeObjectID, bson.TypeBoolean,
		bson.TypeDateTime, bson.TypeNull, bson.TypeInt32, bson.TypeTimestamp,
		bson.TypeInt64, bson.TypeDecimal128, bson.TypeMaxKey, bson.TypeMinKey:
	}

	var value any
	if err := raw.Unmarshal(&value); err != nil {
		return xerror.Unexpected().With(err)
	}
	return budget.ChargeJSON(value)
}
