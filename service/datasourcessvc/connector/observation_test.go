package connector

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	connectorsql "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/stretchr/testify/require"
)

func TestMetadataObserverReconstructsOnlyTheAdmittedRead(t *testing.T) {
	t.Parallel()
	for _, target := range []proofevidence.MetadataObserver{
		hubspot.ContactMetadataObserver{AccountID: 123},
		bigquery.TableMetadataObserver{ProjectID: "fixture", DatasetID: "crm", TableID: "contacts"},
		googleworkspace.WorksheetMetadataObserver{SpreadsheetID: "fixture", WorksheetID: 0, Range: "A1:B2"},
		googleworkspace.FileMetadataObserver{FileID: "fixture", Operation: "read"},
		postgres.TableMetadataObserver{TableMetadataResource: connectorsql.TableMetadataResource{Host: "postgres.example.com", Port: 5432, Database: "analytics", Schema: "public", Table: "orders"}},
		mysql.TableMetadataObserver{TableMetadataResource: connectorsql.TableMetadataResource{Host: "mysql.example.com", Port: 3306, Database: "analytics", Schema: "analytics", Table: "orders"}},
		mongo.CollectionMetadataObserver{Host: "mongo.example.com", Port: 27017, Database: "analytics", Collection: "orders"},
		s3.ObjectMetadataObserver{Region: "eu-west-1", Bucket: "fixture", Key: "orders.json"},
	} {
		plan, err := target.Plan()
		require.NoError(t, err)
		observer, err := NewMetadataObserver(plan)
		require.NoError(t, err)
		actual, err := observer.Plan()
		require.NoError(t, err)
		require.Equal(t, plan, actual)
		for _, mutate := range []func(*proofevidence.MetadataPlan){
			func(p *proofevidence.MetadataPlan) { p.Operation = "insert_rows" },
			func(p *proofevidence.MetadataPlan) { p.Routes[0].Method = "DELETE" },
			func(p *proofevidence.MetadataPlan) { p.Routes[0].URL += "/other" },
			func(p *proofevidence.MetadataPlan) { p.Resource = []byte(`null`) },
			func(p *proofevidence.MetadataPlan) { p.Resource = []byte(`{"unknown":"resource"}`) },
			func(p *proofevidence.MetadataPlan) { p.MaxConnections++ },
		} {
			changed, err := target.Plan()
			require.NoError(t, err)
			mutate(&changed)
			_, err = NewMetadataObserver(changed)
			require.Error(t, err)
		}
	}
}
