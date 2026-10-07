package main

import "testing"

func TestValidateLedgerAnchor(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		anchorID   string
		merkleRoot string
		wantErr    bool
	}{
		{
			name:       "matching anchor",
			response:   `{"anchor_id":"anchor-1","merkle_root":"abcd"}`,
			anchorID:   "anchor-1",
			merkleRoot: "ABCD",
		},
		{
			name:       "wrong anchor",
			response:   `{"anchor_id":"anchor-2","merkle_root":"abcd"}`,
			anchorID:   "anchor-1",
			merkleRoot: "abcd",
			wantErr:    true,
		},
		{
			name:       "wrong root",
			response:   `{"anchor_id":"anchor-1","merkle_root":"ffff"}`,
			anchorID:   "anchor-1",
			merkleRoot: "abcd",
			wantErr:    true,
		},
		{
			name:       "invalid JSON",
			response:   `not-json`,
			anchorID:   "anchor-1",
			merkleRoot: "abcd",
			wantErr:    true,
		},
		{
			name:       "missing fields",
			response:   `{}`,
			anchorID:   "anchor-1",
			merkleRoot: "abcd",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLedgerAnchor([]byte(tt.response), tt.anchorID, tt.merkleRoot)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateLedgerAnchor() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}
