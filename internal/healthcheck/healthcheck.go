package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type HealthCheck interface {
	// ChechHealth should return healthy/unhealthy and an error if it occurred.
	// we separate error here because something could be "unhealthy" but
	// is becoming healthy so we just need to wait.  "error" means that
	// something is wrong and object being healthchecked will not become healthy
	CheckHealth(context.Context, []string) (bool, error)
}

// healthChecks maps a healthcheck name to its implementation. Implementations
// register themselves via init() in their own file.
var healthChecks = map[string]HealthCheck{}

// Perform runs the healthcheck named by args[0], passing the remaining args
// to its CheckHealth implementation.
func Perform(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 {
		return false, errors.New("no arguments given")
	}

	c, ok := healthChecks[strings.ToLower(args[0])]
	if !ok {
		return false, fmt.Errorf("could not find healthcheck for: %s", args[0])
	}

	return c.CheckHealth(ctx, args[1:])
}

func createStatus(err error, fn func() string) string {
	if err == nil {
		return fn()
	}
	return err.Error()
}
