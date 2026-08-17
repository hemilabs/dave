// Copyright (c) 2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

var ErrNoAppropriateBlock = errors.New("could not find an appropriate block")

func GetFirstBlockBeforeTime(ctx context.Context, target uint64, endpoint string) (*types.Block, error) {
	client, err := ethclient.Dial(endpoint)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	block, err := client.BlockByNumber(ctx, big.NewInt(int64(rpc.LatestBlockNumber)))
	if err != nil {
		return nil, err
	}

	for {
		slog.Info("checking block", "number", block.NumberU64(), "hash", block.Hash(), "time", block.Time())

		if block.NumberU64() == 0 {
			return nil, fmt.Errorf("not found: %w", ErrNoAppropriateBlock)
		}

		slog.Info("comparing timstamps", "block time", block.Time(), "target", target)

		// find the first block whose time is less than the target
		if target > block.Time() {
			return block, nil
		}

		// clayton note: use parent
		block, err = client.BlockByHash(ctx, block.Header().ParentHash)
		if err != nil {
			return nil, err
		}
	}
}

func SetHeadToBlock(ctx context.Context, endpoint string, block *types.Block) error {
	rpcClient, err := rpc.Dial(endpoint)
	if err != nil {
		return fmt.Errorf("failed to connect to Geth RPC: %w", err)
	}
	defer rpcClient.Close()

	if err := stopWS(ctx, rpcClient); err != nil {
		return err
	}

	num := fmt.Sprintf("0x%s", strconv.FormatInt(block.Number().Int64(), 16))

	err = rpcClient.CallContext(ctx, nil, "debug_setHead", num)
	if err != nil {
		return fmt.Errorf("failed to call debug_setHead: %w", err)
	}

	return nil
}

// stopWS stops the node's HTTP and WS RPC servers via the admin
// namespace before the destructive debug_setHead rewind is issued, so that
// no other client can observe or interact with the node while its head is
// being manipulated.
//
// admin_stopWS report false, with no error, when the
// respective server was already stopped -- which happens once per
// --backup-strategy entry against the same node, since each entry calls
// SetHeadToBlock in turn -- so that result is treated as already-satisfied,
// not a failure. Ensuring both calls are "successful" means ensuring
// neither call returns an RPC/transport error.
func stopWS(ctx context.Context, rpcClient *rpc.Client) error {
	if err := rpcClient.CallContext(ctx, nil, "admin_stopWS"); err != nil {
		return fmt.Errorf("failed to call admin_stopWS: %w", err)
	}

	return nil
}
