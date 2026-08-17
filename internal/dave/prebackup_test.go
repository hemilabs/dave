// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/go-test/deep"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestGetFirstBlockBeforeTime(t *testing.T) {
	type testTableItem struct {
		name          string
		secondsAgo    int64
		expectedError error
	}

	testTable := []testTableItem{
		{
			name:       "6 seconds ago",
			secondsAgo: 6,
		},
		{
			name:          "error for does not exist",
			secondsAgo:    500,
			expectedError: ErrNoAppropriateBlock,
		},
	}

	for _, testCase := range testTable {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			opGethRpcEndpoint, _ := createOpGethContainer(t)
			t.Logf("opGethRPCEndpoint: %s", opGethRpcEndpoint)

			waitForBlocks(t, t.Context(), 20)

			target := uint64(time.Now().Unix() - int64(testCase.secondsAgo))

			block, err := GetFirstBlockBeforeTime(t.Context(), target, opGethRpcEndpoint)
			if err != nil {
				if testCase.expectedError != nil {
					if !errors.Is(err, testCase.expectedError) {
						t.Fatal(err)
					}
					return
				}
				t.Fatal(err)
			}

			if block.Time() > target {
				t.Fatalf("block time is after target.  block time: %d, target: %d", block.Time(), target)
			}

			ensureParentIsAfterTargetTime(t.Context(), t, block, opGethRpcEndpoint, target)
		})
	}
}

func TestSetHeadToBlock(t *testing.T) {
	type testTableItem struct {
		name       string
		secondsAgo []int64
		// this error is not a type, so we need to check string equals
		expectedErrorStr string
	}

	testTable := []testTableItem{
		{
			name:       "6 seconds ago",
			secondsAgo: []int64{6},
		},
		{
			name:       "6, then 12 seconds ago",
			secondsAgo: []int64{6, 12},
		},
		{
			name:             "12, then 6 seconds ago + error for future block",
			secondsAgo:       []int64{12, 6},
			expectedErrorStr: "not allowed to rewind to a future block",
		},
	}

	for _, testCase := range testTable {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			opGethRpcEndpoint, _ := createOpGethContainer(t)
			t.Logf("opGethRPCEndpoint: %s", opGethRpcEndpoint)

			waitForBlocks(t, t.Context(), 20)

			for _, secondsAgo := range testCase.secondsAgo {
				target := uint64(time.Now().Unix() - int64(secondsAgo))

				block, err := GetFirstBlockBeforeTime(t.Context(), target, opGethRpcEndpoint)
				if err != nil {
					t.Fatal(err)
				}

				if err := SetHeadToBlock(t.Context(), opGethRpcEndpoint, block); err != nil {
					if testCase.expectedErrorStr != "" {
						if !strings.Contains(err.Error(), testCase.expectedErrorStr) {
							t.Fatalf("unexpected error: %s", err)
						}
						return
					} else {
						t.Fatal(err)
					}
				}

				client, err := ethclient.Dial(opGethRpcEndpoint)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()

				head, err := client.BlockByNumber(t.Context(), big.NewInt(int64(rpc.LatestBlockNumber)))
				if err != nil {
					t.Fatal(err)
				}

				if diff := deep.Equal(block, head); len(diff) != 0 {
					t.Fatalf("unexpected diff: %s", diff)
				}
			}
		})
	}
}

// TestBackupStrategiesCLI drives `dave backup --backup-strategy ...` as a
// real subprocess against a real (testcontainers) geth dev node, with no
// mocking. Every strategy uses name "ethsethead" (the only supported
// backup-strategy name), the geth container's RPC endpoint, and a target
// timestamp in the recent past; it asserts one snapshot is stored in the
// repository per --backup-strategy entry (or a single ordinary snapshot
// when none are given). For each snapshot produced by a strategy, it also
// retrieves that snapshot from the (real) MinIO repository, loads it into a
// brand new geth container, and confirms that the new node's chain
// contains the exact block that the strategy's target timestamp resolved
// to at backup time.
func TestBackupStrategiesCLI(t *testing.T) {
	t.Parallel()
	if !testDocker(t) {
		return
	}

	tests := []struct {
		name          string
		numStrategies int
	}{
		{name: "0 backup strategies", numStrategies: 0},
		{name: "1 backup strategy", numStrategies: 1},
		{name: "multiple backup strategies", numStrategies: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Generous timeout: this subtest spins up the source geth
			// container plus, sequentially, one additional geth container
			// per backup strategy to verify its snapshot.
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
			defer cancel()

			opGethRpcEndpoint, hostDataDir := createOpGethContainer(t)
			t.Logf("opGethRPCEndpoint: %s", opGethRpcEndpoint)

			// Give the chain enough history to satisfy the oldest target
			// timestamp used below (worst case 30s ago, plus margin).
			waitForBlocks(t, t.Context(), 45)

			s3Url, _ := createMinIOContainer(t)

			args := []string{"backup", "--repo", "s3:" + s3Url}

			// Resolve, up front, the exact block each strategy's target
			// timestamp should rewind to -- using the same lookup the CLI
			// itself will perform. This must happen before the backup
			// runs: each backup-strategy rewinds the live node further
			// back via debug_setHead, so re-resolving a newer target
			// against the node's endpoint afterwards would incorrectly
			// see the already-rewound (older) head instead.
			now := time.Now()
			wantBlocks := make([]*types.Block, tt.numStrategies)
			for i := range tt.numStrategies {
				target := uint64(now.Unix() - int64(10*(i+1)))

				block, err := GetFirstBlockBeforeTime(ctx, target, opGethRpcEndpoint)
				if err != nil {
					t.Fatalf("get first block before target %d: %v", target, err)
				}
				wantBlocks[i] = block

				args = append(args, "--backup-strategy",
					fmt.Sprintf("ethsethead:%s:%d", opGethRpcEndpoint, target))
			}
			args = append(args, hostDataDir)

			t.Logf("cli args are %s", args)

			stdout, stderr, err := runDaveCLI(t, ctx, t.TempDir(), args...)
			if err != nil {
				t.Fatalf("dave backup: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
			}

			repo, err := NewS3Repository(ctx, s3Url)
			if err != nil {
				t.Fatalf("new S3 repository: %v", err)
			}
			snapshots, err := repo.SnapshotList(ctx)
			if err != nil {
				t.Fatalf("list snapshots: %v", err)
			}

			wantSnapshots := tt.numStrategies
			if wantSnapshots == 0 {
				// No strategies: a single ordinary backup still runs.
				wantSnapshots = 1
			}
			if len(snapshots) != wantSnapshots {
				t.Fatalf("got %d snapshots, want %d (stdout: %s)", len(snapshots), wantSnapshots, stdout)
			}

			if tt.numStrategies == 0 {
				// No backup strategy was configured, so there is no
				// target block to verify against.
				return
			}

			// repo.SnapshotList (above) returns newest-created first;
			// re-sort oldest-created first so index i lines up with
			// wantBlocks[i]: strategies are processed newest-target-first
			// (see the loop above), so their snapshots are also created
			// in that same order.
			byCreationOrder := slices.Clone(snapshots)
			slices.SortFunc(byCreationOrder, func(a, b *Snapshot) int {
				return a.Time.Compare(b.Time)
			})

			for i, wantBlock := range wantBlocks {
				snapshotID := byCreationOrder[i].ID
				t.Logf("verifying snapshot %d (%s) contains block %d (%s)",
					i, snapshotID, wantBlock.NumberU64(), wantBlock.Hash())

				dest := t.TempDir()
				if err := repo.SnapshotRetrieve(ctx, snapshotID, dest, nil); err != nil {
					t.Fatalf("retrieve snapshot %d (%s): %v", i, snapshotID, err)
				}

				// SnapshotRetrieve extracts the archive under the
				// basename of the directory that was originally backed
				// up (hostDataDir here), recreating an equivalent geth
				// data directory.
				restoredDataDir := filepath.Join(dest, filepath.Base(hostDataDir))

				restoredRpcEndpoint := startOpGethContainer(t, restoredDataDir)
				t.Logf("restored opGethRPCEndpoint for snapshot %d: %s", i, restoredRpcEndpoint)

				client, err := ethclient.Dial(restoredRpcEndpoint)
				if err != nil {
					t.Fatalf("dial restored geth node: %v", err)
				}
				defer client.Close()

				select {
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				case <-time.After(5 * time.Second):
				}

				gotBlock, err := client.BlockByHash(ctx, wantBlock.Hash())
				if err != nil {
					t.Fatalf("snapshot %d: restored node does not contain block %s: %v",
						i, wantBlock.Hash(), err)
				}

				if diff := deep.Equal(wantBlock, gotBlock); len(diff) != 0 {
					t.Fatalf("snapshot %d: restored block differs from expected: %s", i, diff)
				}
			}
		})
	}
}

const dockerImageGeth = "ethereum/client-go:v1.17.5@sha256:523d3ba26623a619e912019068dc2784f02934070ac46bdae4d5b9df0d917814"

func waitForBlocks(t *testing.T, ctx context.Context, count int) {
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-time.After(time.Duration(count) * time.Second): // 1 block per second
	}
}

type LogConsumer struct {
	t *testing.T
}

func (l *LogConsumer) Accept(log testcontainers.Log) {
	l.t.Logf("%s: %s", log.LogType, string(log.Content))
}

// createOpGethContainer creates a fresh, empty host data directory and
// starts a geth dev-mode container bound to it. It returns the container's
// RPC endpoint and the host data directory.
func createOpGethContainer(t *testing.T) (string, string) {
	t.Helper()

	hostDataDir := t.TempDir()

	return startOpGethContainer(t, hostDataDir), hostDataDir
}

// startOpGethContainer starts a geth dev-mode container bound to
// hostDataDir and returns its RPC endpoint. hostDataDir may be empty, or
// may already contain geth chain data (e.g. restored from a snapshot), in
// which case the node resumes from the existing chain rather than starting
// a new one.
func startOpGethContainer(t *testing.T, hostDataDir string) string {
	t.Helper()

	// Type assert the address to access the Port field
	// Pick the host port ourselves, up front, and bind it explicitly
	// (rather than letting Docker assign a random one via ExposedPorts)
	// so the endpoint stays valid across a container stop/start: a
	// Docker-assigned mapping is only guaranteed for the lifetime of the
	// container's current start, whereas an explicit binding is part of
	// the container's config and is reapplied on every start.

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	// Type assert the address to access the Port field
	addr := listener.Addr().(*net.TCPAddr)
	port := addr.Port
	listener.Close()
	time.Sleep(1 * time.Second)
	hostPort := port
	t.Logf("will use port on host machine: %d", hostPort)

	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dockerImageGeth,
			ExposedPorts: []string{"8545/tcp"},
			Cmd: []string{
				"--dev",
				"--dev.period", "2",
				"--datadir", "/data",
				"--http",
				"--http.addr", "0.0.0.0",
				"--http.port", "8545",
				// "admin" is required so admin_stopHTTP/admin_stopWS
				// (called by SetHeadToBlock) are reachable.
				"--http.api", "eth,net,web3,debug,admin",
				"--http.vhosts", "*",
				"--http.corsdomain", "*",
				// A WS server is started (though never exposed/dialed by
				// tests) purely so admin_stopWS has a real server to stop.
				"--ws",
				"--ws.addr", "0.0.0.0",
				"--ws.port", "8546",
				"--ws.api", "eth,net,web3,debug,admin",
				"--ws.origins", "*",
				"--nodiscover",
				"--ipcdisable",
			},
			WaitingFor: wait.ForExposedPort(),
			LogConsumerCfg: &testcontainers.LogConsumerConfig{
				Consumers: []testcontainers.LogConsumer{
					&LogConsumer{
						t: t,
					},
				},
			},
			ConfigModifier: func(c *container.Config) {
				c.User = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Binds = []string{hostDataDir + ":/data"}
				hc.PortBindings = network.PortMap{
					network.MustParsePort("8545/tcp"): []network.PortBinding{
						{
							HostIP:   netip.MustParseAddr("0.0.0.0"),
							HostPort: fmt.Sprintf("%d", hostPort),
						},
					},
				}
			},
		},
		Started: true,
	})

	t.Cleanup(func() {
		if err := c.Terminate(context.Background()); err != nil {
			t.Fatalf("terminate geth container: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("create geth container: %v", err)
	}

	host, err := c.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf("http://%s:%d", host, hostPort)
}

func ensureParentIsAfterTargetTime(ctx context.Context, t *testing.T, blockUnderTest *types.Block, endpoint string, target uint64) {
	client, err := ethclient.Dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	block, err := client.BlockByNumber(ctx, big.NewInt(int64(rpc.LatestBlockNumber)))
	if err != nil {
		t.Fatal(err)
	}

	for {
		if block.NumberU64() == 0 {
			t.Fatal("could not find an appropriate block in test")
		}

		if block.ParentHash().String() == blockUnderTest.Hash().String() {
			t.Logf(
				"checking blocks target: %d \nblock under test: %s:%d @ %d\n parent block: %s:%d @ %d",
				target, blockUnderTest.Hash().String(), blockUnderTest.NumberU64(), blockUnderTest.Time(),
				block.Hash().String(), block.NumberU64(), block.Time())
			if target > block.Time() {
				t.Fatal("block was skipped")
			}
			return
		}

		block, err = client.BlockByNumber(ctx, big.NewInt(int64(block.NumberU64()-1)))
		if err != nil {
			t.Fatal(err)
		}
	}
}
