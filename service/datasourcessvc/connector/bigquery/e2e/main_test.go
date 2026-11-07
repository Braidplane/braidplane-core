package bigquery_test

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/containerfixture"
)

func TestMain(m *testing.M) {
	os.Exit(containerfixture.Run(m, "bigquery-connector-test"))
}
