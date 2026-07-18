package main

import "testing"

func TestParsePortSpec(t *testing.T) {
	tests := []struct {
		in         string
		wantLocal  int
		wantRemote int
		wantErr    bool
	}{
		{"5432", 5432, 5432, false},
		{"8080:80", 8080, 80, false},
		{"", 0, 0, true},
		{"0", 0, 0, true},
		{"-1", 0, 0, true},
		{"abc", 0, 0, true},
		{"8080:", 0, 0, true},
		{"8080:80:90", 0, 0, true},
		{"80:0", 0, 0, true},
		{"70000", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			local, remote, err := parsePortSpec(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePortSpec(%q) = (%d,%d,nil), want error", tt.in, local, remote)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortSpec(%q) unexpected error: %v", tt.in, err)
			}
			if local != tt.wantLocal || remote != tt.wantRemote {
				t.Errorf("parsePortSpec(%q) = (%d,%d), want (%d,%d)", tt.in, local, remote, tt.wantLocal, tt.wantRemote)
			}
		})
	}
}
