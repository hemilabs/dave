// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// opGethClient is a minimal Ethereum JSON-RPC client, used to locate a
// historical block by approximate timestamp and roll op-geth's head back to
// it via the debug_setHead method.
type opGethClient struct {
	url        string
	httpClient *http.Client
}

func newOpGethClient(url string, httpClient *http.Client) *opGethClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &opGethClient{url: url, httpClient: httpClient}
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// call performs a single JSON-RPC request and decodes its result into v.
func (c *opGethClient) call(ctx context.Context, method string, params []any, v any) error {
	reqBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	})
	if err != nil {
		return fmt.Errorf("marshal json-rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform http request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d from %s", res.StatusCode, c.url)
	}

	var rpcRes jsonRPCResponse
	if err := json.NewDecoder(res.Body).Decode(&rpcRes); err != nil {
		return fmt.Errorf("decode json-rpc response: %w", err)
	}
	if rpcRes.Error != nil {
		return fmt.Errorf("json-rpc error calling %s: %s", method, rpcRes.Error.Message)
	}

	if v == nil {
		return nil
	}
	if err := json.Unmarshal(rpcRes.Result, v); err != nil {
		return fmt.Errorf("decode json-rpc result: %w", err)
	}
	return nil
}

type ethBlock struct {
	Number     string `json:"number"`
	Timestamp  string `json:"timestamp"`
	ParentHash string `json:"parentHash"`
}

// blockByNumber fetches the block header at the given tag ("latest",
// "earliest", or a hex-encoded block number), returning its number, unix
// timestamp, and parent hash.
func (c *opGethClient) blockByNumber(ctx context.Context, tag string) (number, timestamp uint64, parentHash string, err error) {
	var block ethBlock
	if err = c.call(ctx, "eth_getBlockByNumber", []any{tag, false}, &block); err != nil {
		return 0, 0, "", err
	}
	return parseEthBlock(block, tag)
}

// blockByHash fetches the block header with the given hash, returning its
// number, unix timestamp, and parent hash.
func (c *opGethClient) blockByHash(ctx context.Context, hash string) (number, timestamp uint64, parentHash string, err error) {
	var block ethBlock
	if err = c.call(ctx, "eth_getBlockByHash", []any{hash, false}, &block); err != nil {
		return 0, 0, "", err
	}
	return parseEthBlock(block, hash)
}

// parseEthBlock parses the number and timestamp out of an ethBlock's
// hex-encoded fields. ref identifies the block in error messages (the tag or
// hash it was fetched by).
func parseEthBlock(block ethBlock, ref string) (number, timestamp uint64, parentHash string, err error) {
	if block.Number == "" || block.Timestamp == "" {
		return 0, 0, "", fmt.Errorf("block %q not found", ref)
	}
	if number, err = parseHexUint(block.Number); err != nil {
		return 0, 0, "", fmt.Errorf("parse block number: %w", err)
	}
	if timestamp, err = parseHexUint(block.Timestamp); err != nil {
		return 0, 0, "", fmt.Errorf("parse block timestamp: %w", err)
	}
	return number, timestamp, block.ParentHash, nil
}

// closestBlockTolerance is how close, in seconds, a block's timestamp must
// be to the requested target time for findClosestBlock to accept it.
const closestBlockTolerance = 12

// findClosestBlock searches backward from the chain tip, following each
// block's parent hash one block at a time, for a block whose timestamp is
// within closestBlockTolerance seconds of the given target unix time. Block
// timestamps are assumed to be monotonically non-decreasing, so once a
// block's timestamp drops at-or-below target without satisfying the
// tolerance, no earlier block can either.
func (c *opGethClient) findClosestBlock(ctx context.Context, target int64) (number, timestamp uint64, err error) {
	var parentHash string
	if number, timestamp, parentHash, err = c.blockByNumber(ctx, "latest"); err != nil {
		return 0, 0, fmt.Errorf("get latest block: %w", err)
	}

	for {
		if absDiff(int64(timestamp), target) <= closestBlockTolerance {
			return number, timestamp, nil
		}
		if timestamp <= uint64(target) || number == 0 {
			return 0, 0, fmt.Errorf("no block within %ds of target %d found (closest: block %d at %d)",
				closestBlockTolerance, target, number, timestamp)
		}

		if number, timestamp, parentHash, err = c.blockByHash(ctx, parentHash); err != nil {
			return 0, 0, fmt.Errorf("get parent block %s: %w", parentHash, err)
		}
	}
}

// setHead rolls op-geth's head back to the given block number via the
// debug_setHead JSON-RPC method.
func (c *opGethClient) setHead(ctx context.Context, number uint64) error {
	return c.call(ctx, "debug_setHead", []any{hexUint(number)}, nil)
}

func hexUint(n uint64) string {
	return fmt.Sprintf("0x%x", n)
}

func parseHexUint(s string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hex value %q: %w", s, err)
	}
	return n, nil
}

func absDiff(a, b int64) int64 {
	if a < b {
		return b - a
	}
	return a - b
}

// callWithRetry retries fn until it succeeds or ctx expires, backing off
// between attempts. It is used for the first RPC calls after (re)starting
// op-geth, since the container may report "running" slightly before its
// JSON-RPC server is actually accepting connections.
func callWithRetry(ctx context.Context, fn func() error) error {
	const retryInterval = 2 * time.Second

	var err error
	for {
		if err = fn(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last error: %w)", ctx.Err(), err)
		case <-time.After(retryInterval):
		}
	}
}
