// healthcheck probes a container's local readiness endpoint without shell tools.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return xerror.ValidationFailed().Detail("usage: braidplane-healthcheck <port>")
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		return xerror.ValidationFailed().Detail("port must be between 1 and 65535")
	}
	client := &http.Client{
		Timeout:       2 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	// #nosec G704 -- The host is fixed loopback; only a validated numeric port is supplied.
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/readyz", nil)
	if err != nil {
		return xerror.Wrapf("create local readiness request: %w", err)
	}
	// #nosec G704 -- This request can target only the validated loopback endpoint above.
	response, err := client.Do(request)
	if err != nil {
		return xerror.Wrapf("local readiness request failed: %w", err)
	}
	if err := response.Body.Close(); err != nil {
		return xerror.Wrapf("close local readiness response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return xerror.ExecutionUnavailable().Detailf("local readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}
