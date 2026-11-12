package e2e

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/containerfixture"
)

func TestMain(m *testing.M) {
	if (os.Getenv("BP_COMPLETION_CRASH_WORKER") == "1" || os.Getenv("BP_SINK_CRASH_WORKER") == "1" || os.Getenv("BP_NATIVE_FILE_EXECUTOR_WORKER") == "1") && os.Getenv("DATABASE_URL") != "" {
		os.Exit(m.Run())
	}
	os.Exit(containerfixture.Run(m, "database", "s3-connector-test"))
}
