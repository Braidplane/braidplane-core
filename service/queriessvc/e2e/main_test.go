package e2e

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/containerfixture"
)

func TestMain(m *testing.M) {
	os.Exit(containerfixture.Run(m, "database", "postgres-connector-test", "mysql-connector-test", "mongo-connector-test", "http-connector-test", "hubspot-connector-test", "git-connector-test", "s3-connector-test"))
}
