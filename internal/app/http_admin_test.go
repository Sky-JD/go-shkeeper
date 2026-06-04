package app

import "testing"

func TestWalletStatusLabelAndClass(t *testing.T) {
	tests := []struct {
		status string
		label  string
		class  string
	}{
		{status: "Synced", label: "已同步", class: "ok"},
		{status: "Offline", label: "离线", class: "err"},
		{status: "Sync In Progress (12 blocks behind)", label: "同步中 (12 个区块落后)", class: "warn"},
		{status: "Sync In Progress (98.5%)", label: "同步中 (98.5%)", class: "warn"},
		{status: "Unknown", label: "Unknown", class: "muted"},
	}
	for _, tt := range tests {
		if got := walletStatusLabel(tt.status); got != tt.label {
			t.Fatalf("walletStatusLabel(%q)=%q want %q", tt.status, got, tt.label)
		}
		if got := walletStatusClass(tt.status); got != tt.class {
			t.Fatalf("walletStatusClass(%q)=%q want %q", tt.status, got, tt.class)
		}
	}
}
