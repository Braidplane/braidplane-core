package sql

import (
	"encoding/json"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	parsermysql "github.com/pingcap/tidb/pkg/parser/mysql"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// MetadataStatementMatches authorizes only a statically identified relation.
// Dialect syntax the installed parser cannot resolve remains unverified.
func MetadataStatementMatches(kind model.DataSourceType, resource TableMetadataResource, raw json.RawMessage, source bool) bool {
	var statement struct{ Type, SQL string }
	if json.Unmarshal(raw, &statement) != nil || statement.Type != "" && statement.Type != kind.String() || len(statement.SQL) > 1<<20 {
		return false
	}
	p := parser.New()
	if kind == model.DataSourceTypePostgres {
		p.SetSQLMode(parsermysql.ModeANSIQuotes)
	}
	statements, _, err := p.ParseSQL(statement.SQL)
	if err != nil || len(statements) != 1 {
		return false
	}
	switch statement := statements[0].(type) {
	case *ast.SelectStmt, *ast.SetOprStmt:
		if !source {
			return false
		}
	case *ast.InsertStmt:
		if source {
			return false
		}
		return metadataWriteTargetMatches(statement.Table, kind, resource)
	case *ast.UpdateStmt:
		if source || statement.MultipleTable || statement.With != nil {
			return false
		}
		return metadataWriteTargetMatches(statement.TableRefs, kind, resource)
	case *ast.DeleteStmt:
		if source || statement.IsMultiTable || statement.With != nil {
			return false
		}
		return metadataWriteTargetMatches(statement.TableRefs, kind, resource)
	default:
		return false
	}
	visitor := &metadataRelation{resource: resource, kind: kind, ctes: make(map[string]bool)}
	_, valid := statements[0].Accept(visitor)
	return valid && visitor.found
}

func metadataWriteTargetMatches(refs *ast.TableRefsClause, kind model.DataSourceType, resource TableMetadataResource) bool {
	if refs == nil || refs.TableRefs == nil || refs.TableRefs.Right != nil {
		return false
	}
	source, ok := refs.TableRefs.Left.(*ast.TableSource)
	if !ok {
		return false
	}
	table, ok := source.Source.(*ast.TableName)
	return ok && metadataTableMatches(table, kind, resource)
}

func metadataTableMatches(table *ast.TableName, kind model.DataSourceType, resource TableMetadataResource) bool {
	schema := table.Schema.O
	if schema == "" && kind == model.DataSourceTypeMySQL {
		schema = resource.Database
	}
	return schema == resource.Schema && table.Name.O == resource.Table
}

type metadataRelation struct {
	resource TableMetadataResource
	kind     model.DataSourceType
	found    bool
	ctes     map[string]bool
}

func (v *metadataRelation) Enter(node ast.Node) (ast.Node, bool) {
	if with, ok := node.(*ast.WithClause); ok {
		for _, cte := range with.CTEs {
			v.ctes[strings.ToLower(cte.Name.O)] = true
		}
	}
	if table, ok := node.(*ast.TableName); ok {
		if table.Schema.O == "" && v.ctes[strings.ToLower(table.Name.O)] {
			return node, false
		}
		if metadataTableMatches(table, v.kind, v.resource) {
			v.found = true
		}
	}
	return node, false
}
func (v *metadataRelation) Leave(node ast.Node) (ast.Node, bool) { return node, true }
