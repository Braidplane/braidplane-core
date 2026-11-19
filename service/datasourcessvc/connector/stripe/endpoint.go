//go:build !stripe_bruno

package stripe

import (
	"net/http"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
)

func apiEndpoint(_ string) (*string, http.RoundTripper, error) {
	return nil, &http.Transport{DialContext: hostguard.DialPublicHost, ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second}, nil
}
