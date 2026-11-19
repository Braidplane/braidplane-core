//go:build stripe_bruno

package stripe

import (
	"net/http"
	"os"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Only the Bruno test binary can send the synthetic key to the loopback mock.
func apiEndpoint(key string) (*string, http.RoundTripper, error) {
	port, err := strconv.ParseUint(os.Getenv("STRIPE_MOCK_PORT"), 10, 16)
	if err != nil || port == 0 || key != "rk_test_bruno" {
		return nil, nil, xerror.InvalidCredentials().Detail("Stripe Bruno requires its fixture key and loopback port")
	}
	url := "http://127.0.0.1:" + strconv.FormatUint(port, 10)
	return &url, &http.Transport{}, nil
}
