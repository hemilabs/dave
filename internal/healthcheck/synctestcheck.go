package healthcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

type SynctestCheck struct{}

func init() {
	healthChecks["synctest"] = &SynctestCheck{}
}

type ethBlockByNumberResponse struct {
	Result struct {
		Hash string `json:"hash"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *SynctestCheck) CheckHealth(ctx context.Context, args []string) (bool, error) {
	if len(args) != 2 {
		return false, fmt.Errorf("unexpected number of args, got %d, expected 2", len(args))
	}

	controlURL := args[0]
	experimentalURL := args[1]

	controlHash, err := s.latestBlockHash(ctx, controlURL)
	if err != nil {
		return false, fmt.Errorf("get latest block from control url: %w", err)
	}

	experimentalHash, err := s.latestBlockHash(ctx, experimentalURL)
	if err != nil {
		return false, fmt.Errorf("get latest block from experimental url: %w", err)
	}

	if controlHash != experimentalHash {
		return false, nil
	}

	return true, nil
}

// latestBlockHash fetches the hash of the "latest" block from the given
// Ethereum JSON-RPC endpoint.
func (s *SynctestCheck) latestBlockHash(ctx context.Context, url string) (string, error) {
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "eth_getBlockByNumber",
		"params":  []any{"latest", false},
		"id":      1,
	})
	if err != nil {
		return "", fmt.Errorf("could not marshal json-rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("could not create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := http.Client{}

	res, err := httpClient.Do(req)
	slog.Debug("Node latest block status",
		"method", req.Method, "url", url,
		"status", createStatus(err, func() string {
			return res.Status
		}))
	if err != nil {
		return "", fmt.Errorf("error performing http request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d from %s", res.StatusCode, url)
	}

	var rpcRes ethBlockByNumberResponse
	if err := json.NewDecoder(res.Body).Decode(&rpcRes); err != nil {
		return "", fmt.Errorf("could not decode json-rpc response: %w", err)
	}
	if rpcRes.Error != nil {
		return "", fmt.Errorf("json-rpc error: %s", rpcRes.Error.Message)
	}

	return rpcRes.Result.Hash, nil
}
