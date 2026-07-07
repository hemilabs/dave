package healthcheck

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
)

type URLCheck struct{}

func init() {
	healthChecks["url"] = &URLCheck{}
}

func (u *URLCheck) CheckHealth(ctx context.Context, args []string) (bool, error) {
	if len(args) != 1 {
		return false, fmt.Errorf("expected args to be len == 1, received %d", len(args))
	}

	url := args[0]

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("could not create http request: %w", err)
	}

	httpClient := http.Client{}

	res, err := httpClient.Do(req)
	slog.Debug("Node health status",
		"method", req.Method, "url", url,
		"status", createStatus(err, func() string {
			return strconv.Itoa(res.StatusCode)
		}))
	if err != nil {
		return false, fmt.Errorf("error performing http request: %s", err)
	}

	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Println("bad status code")
		return false, nil
	}

	return true, nil
}
