// Copyright (c) 2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package healthcheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newBlockServer returns a test HTTP server that behaves as an Ethereum
// JSON-RPC endpoint for eth_getBlockByNumber. If fail is true, the server
// responds with an error status instead of a block.
func newBlockServer(t *testing.T, hash string, fail bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		res := ethBlockByNumberResponse{}
		res.Result.Hash = hash

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res); err != nil {
			t.Errorf("could not encode response: %v", err)
		}
	}))
}

func TestSynctestCheckCheckHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		controlHash string
		controlFail bool
		expHash     string
		expFail     bool
		want        bool
		wantErr     bool
	}{
		{
			name:        "tips match",
			controlHash: "0xabc",
			expHash:     "0xabc",
			want:        true,
		},
		{
			name:        "tips do not match",
			controlHash: "0xabc",
			expHash:     "0xdef",
			want:        false,
		},
		{
			name:        "controlUrl returns an error",
			controlFail: true,
			expHash:     "0xabc",
			want:        false,
			wantErr:     true,
		},
		{
			name:        "experimentalUrl returns an error",
			controlHash: "0xabc",
			expFail:     true,
			want:        false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			controlSrv := newBlockServer(t, tt.controlHash, tt.controlFail)
			defer controlSrv.Close()

			expSrv := newBlockServer(t, tt.expHash, tt.expFail)
			defer expSrv.Close()

			s := SynctestCheck{}
			got, err := s.CheckHealth(t.Context(), []string{controlSrv.URL, expSrv.URL})
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckHealth() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("CheckHealth() = %v, want %v", got, tt.want)
			}
		})
	}
}
