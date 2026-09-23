// Copyright (c) 2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

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
		Hash       string `json:"hash"`
		ParentHash string `json:"parentHash"`
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

	controlHash, parentHash, err := s.blockByHash(ctx, controlURL, "")
	if err != nil {
		return false, fmt.Errorf("get latest block from control url: %w", err)
	}

	experimentalHash, _, err := s.blockByHash(ctx, experimentalURL, "")
	if err != nil {
		return false, fmt.Errorf("get latest block from experimental url: %w", err)
	}

	// check if we're within 5 blocks from the tip
	for i := range 5 {
		slog.Debug("Comparing blocks", "depth", i, "controlHash", controlHash)

		if controlHash == experimentalHash {
			return true, nil
		}

		controlHash, parentHash, err = s.blockByHash(ctx, controlURL, parentHash)
		if err != nil {
			return false, fmt.Errorf("error getting control block parent by hash: %w", err)
		}
	}

	return false, nil
}

// blockByHash fetches a block with a specific hash from the given
// Ethereum JSON-RPC endpoint. If no hash is provided, it requests
// the latest block.
func (s *SynctestCheck) blockByHash(ctx context.Context, url string, hash string) (string, string, error) {
	method := "eth_getBlockByHash"
	param := any(hash)
	if hash == "" {
		method = "eth_getBlockByNumber"
		param = "latest"
	}
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  []any{param, false},
		"id":      1,
	})
	if err != nil {
		return "", "", fmt.Errorf("could not marshal json-rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("could not create http request: %w", err)
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
		return "", "", fmt.Errorf("error performing http request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status code %d from %s", res.StatusCode, url)
	}

	var rpcRes ethBlockByNumberResponse
	if err := json.NewDecoder(res.Body).Decode(&rpcRes); err != nil {
		return "", "", fmt.Errorf("could not decode json-rpc response: %w", err)
	}
	if rpcRes.Error != nil {
		return "", "", fmt.Errorf("json-rpc error: %s", rpcRes.Error.Message)
	}

	return rpcRes.Result.Hash, rpcRes.Result.ParentHash, nil
}
