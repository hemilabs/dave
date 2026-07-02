// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package healthcheck

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestURLCheckCheckHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{
			name:       "200 is healthy",
			statusCode: http.StatusOK,
			want:       true,
		},
		{
			name:       "non-200 is unhealthy",
			statusCode: http.StatusInternalServerError,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer srv.Close()

			u := URLCheck{}
			got, err := u.CheckHealth(t.Context(), []string{srv.URL})
			if err != nil {
				t.Errorf("CheckHealth() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("CheckHealth() = %v, want %v", got, tt.want)
			}
		})
	}
}
