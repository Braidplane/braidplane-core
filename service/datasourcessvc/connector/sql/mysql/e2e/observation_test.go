package mysql_test

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/stretchr/testify/require"
)

func TestSelectedTableMetadataReadsNoRecords(t *testing.T) {
	config := getTestConfig()
	setup, err := connector.New(transportsecurity.WithPlaintext(t.Context()), config)
	require.NoError(t, err)
	defer func() { _ = setup.Close() }()
	_, err = executeSQL(t.Context(), setup, "CREATE TABLE proof_selected_metadata (id INT NOT NULL, name VARCHAR(20))", nil)
	require.NoError(t, err)
	defer func() { _, _ = executeSQL(t.Context(), setup, "DROP TABLE proof_selected_metadata", nil) }()
	port, err := strconv.ParseUint(strconv.Itoa(config.Port), 10, 16)
	require.NoError(t, err)
	observer := connector.TableMetadataObserver{TableMetadataResource: sqlconnector.TableMetadataResource{Host: config.Host, Port: uint16(port), Database: config.Database, Schema: config.Database, Table: "proof_selected_metadata"}}
	plan, err := observer.Plan()
	require.NoError(t, err)
	// #nosec G117 -- Local fixture credential JSON is passed only to the observer.
	credential, err := json.Marshal(connector.Credentials{Username: config.User, Password: &config.Password})
	require.NoError(t, err)
	limits := proofevidence.Limits{Timeout: 5 * time.Second, MaxRows: 2, MaxBytes: 1 << 20}
	transport := func() *testutil.ProofNativeTransport {
		return &testutil.ProofNativeTransport{Plan: plan, Address: net.JoinHostPort(config.Host, strconv.Itoa(config.Port))}
	}
	raw, rows, outcome := observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), transport(), credential, limits)
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
	require.Equal(t, int64(2), rows)
	var snapshot sqlconnector.TableMetadataSnapshot
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, "proof_selected_metadata", snapshot.Table)
	require.Equal(t, "id", snapshot.Columns[0].Name)
	require.False(t, snapshot.Columns[0].Nullable)
	verdict, diagnostics, err := observer.AssessMetadata(raw)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictPassed, verdict)
	require.Empty(t, diagnostics)
	other := observer
	other.Table = "other_table"
	_, _, err = other.AssessMetadata(raw)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	limits.MaxRows = 1
	raw, _, outcome = observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), transport(), credential, limits)
	require.Equal(t, proofevidence.OutcomeOverBudget, outcome)
	require.Nil(t, raw)
	observer.Table = "proof_missing_table"
	plan, err = observer.Plan()
	require.NoError(t, err)
	raw, _, outcome = observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), transport(), credential, limits)
	require.Equal(t, proofevidence.OutcomePartial, outcome)
	require.Nil(t, raw)
}

func TestPasswordlessConnectionAndSelectedTableMetadata(t *testing.T) {
	config := getTestConfig()
	config.User, config.Password = "metadata_reader", ""
	conn, err := connector.New(transportsecurity.WithPlaintext(t.Context()), config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	require.NoError(t, conn.TestConnection(t.Context()))

	port, err := strconv.ParseUint(strconv.Itoa(config.Port), 10, 16)
	require.NoError(t, err)
	observer := connector.TableMetadataObserver{TableMetadataResource: sqlconnector.TableMetadataResource{Host: config.Host, Port: uint16(port), Database: config.Database, Schema: config.Database, Table: "users"}}
	plan, err := observer.Plan()
	require.NoError(t, err)
	limits := proofevidence.Limits{Timeout: 5 * time.Second, MaxRows: 20, MaxBytes: 1 << 20}
	for _, tt := range []struct {
		name       string
		credential string
		outcome    proofevidence.Outcome
	}{
		{"empty password", `{"username":"metadata_reader","password":""}`, proofevidence.OutcomeComplete},
		{"wrong password", `{"username":"metadata_reader","password":"wrong"}`, proofevidence.OutcomeProviderError},
		{"missing password", `{"username":"metadata_reader"}`, proofevidence.OutcomeDenied},
		{"null password", `{"username":"metadata_reader","password":null}`, proofevidence.OutcomeDenied},
		{"missing username", `{"password":""}`, proofevidence.OutcomeDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &testutil.ProofNativeTransport{Plan: plan, Address: net.JoinHostPort(config.Host, strconv.Itoa(config.Port))}
			raw, rows, outcome := observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), transport, []byte(tt.credential), limits)
			require.Equal(t, tt.outcome, outcome)
			if outcome == proofevidence.OutcomeDenied {
				require.Zero(t, transport.Dials)
			}
			if outcome != proofevidence.OutcomeComplete {
				require.Nil(t, raw)
				return
			}
			require.Equal(t, int64(9), rows)
			var snapshot sqlconnector.TableMetadataSnapshot
			require.NoError(t, json.Unmarshal(raw, &snapshot))
			require.Equal(t, "users", snapshot.Table)
			require.Len(t, snapshot.Columns, 9)
			verdict, diagnostics, err := observer.AssessMetadata(raw)
			require.NoError(t, err)
			require.Equal(t, model.ProofVerdictPassed, verdict)
			require.Empty(t, diagnostics)
		})
	}
}
