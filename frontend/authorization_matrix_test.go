package frontend

import (
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommunityAuthorizationMatrixReferencesCoreOperations(t *testing.T) {
	restFile, err := os.Open("../service/authorization/operations.csv")
	require.NoError(t, err)
	defer func() { require.NoError(t, restFile.Close()) }()
	restRows, err := csv.NewReader(restFile).ReadAll()
	require.NoError(t, err)
	operations := make(map[string]bool, len(restRows)-1)
	for _, row := range restRows[1:] {
		operations[row[0]] = true
	}

	uiRows, err := csv.NewReader(strings.NewReader(uiAuthorizationMatrix)).ReadAll()
	require.NoError(t, err)
	for _, row := range uiRows[1:] {
		for _, action := range strings.Split(row[3], ", ") {
			if strings.HasPrefix(action, "local: ") || (row[2] == "retired" && action == "404") {
				continue
			}
			require.True(t, operations[action], "%s %s refers to unknown operation %q", row[0], row[1], action)
		}
	}
}
