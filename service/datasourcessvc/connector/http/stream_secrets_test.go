package http

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestStreamingCredentialScannerAcrossEveryByteBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, secret, body string
		blocked            bool
	}{
		{"raw", "credential", "binary credential body", true},
		{"unicode escape", "credential", `{"value":"\u0063\u0072edential"}`, true},
		{"surrogate pair", "🔑", `{"value":"\ud83d\udd11"}`, true},
		{"unpaired high", "�", `{"value":"\ud800"}`, true},
		{"unpaired low", "�", `{"value":"\udc00"}`, true},
		{"separate values", "credential", `{"a":"cred","b":"ential"}`, false},
		{"overlap fallback", "aaab", "aaaaab", true},
		{"absent", "credential", `{"value":"ordinary file"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := newSecretStreamWriter(&output, []string{tc.secret})
			var err error
			for i := range len(tc.body) {
				_, err = writer.Write([]byte(tc.body[i : i+1]))
				if err != nil {
					break
				}
			}
			if tc.blocked {
				require.ErrorIs(t, err, xerror.HTTPAcknowledgementUncertain())
				_, err = writer.Write([]byte("more"))
				require.ErrorIs(t, err, xerror.HTTPAcknowledgementUncertain())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.body, output.String())
			}
		})
	}
}

func TestStreamingCredentialScannerRecognizesJSONEscapes(t *testing.T) {
	t.Parallel()
	for _, secret := range []string{"quote\"slash\\", "line\nbreak\ttab", "λ🔑", "ababab"} {
		encoded, err := json.Marshal(map[string]string{"value": secret})
		require.NoError(t, err)
		writer := newSecretStreamWriter(&bytes.Buffer{}, []string{secret})
		for _, c := range encoded {
			_, err = writer.Write([]byte{c})
			if err != nil {
				break
			}
		}
		require.ErrorIs(t, err, xerror.HTTPAcknowledgementUncertain())
	}
}
