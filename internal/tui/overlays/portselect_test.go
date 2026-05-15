package overlays

import (
	"testing"

	"github.com/billygate/kap-toolsbox/internal/kube"
)

func TestParsePort(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"5432 (postgresql)", 5432, false},
		{"8080", 8080, false},
		{"  9090 (prometheus)  ", 9090, false},
		{"", 0, true},
		{"not-a-number", 0, true},
		{"(parens-only)", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParsePort(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePort(%q) err = %v, wantErr = %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParsePort(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildPortChoices(t *testing.T) {
	detected := []kube.ContainerPort{
		{Name: "http", Port: 8080},
		{Name: "metrics", Port: 9090},
	}
	choices := BuildPortChoices(detected)

	if !contains(choices, "── PRIORITY ──") {
		t.Error("missing PRIORITY separator")
	}
	if !contains(choices, "5432 (postgresql)") {
		t.Error("missing priority port 5432")
	}
	if !contains(choices, "── DETECTED ──") {
		t.Error("missing DETECTED separator")
	}
	if !contains(choices, "9090 (metrics)") {
		t.Error("missing detected port 9090")
	}
	if !contains(choices, "── COMMON ──") {
		t.Error("missing COMMON separator")
	}
	count := 0
	for _, c := range choices {
		if c == "8080 (http-alt)" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("8080 appears %d times, want 1 (dedup failure)", count)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestIsCustomPortChoice(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"custom (edit ports)", true},
		{"  custom (edit ports)  ", true},
		{"5432 (postgresql)", false},
		{"── PRIORITY ──", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := IsCustomPortChoice(c.in); got != c.want {
				t.Errorf("IsCustomPortChoice(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestBuildPortChoices_CustomFirst(t *testing.T) {
	choices := BuildPortChoices(nil)
	if len(choices) < 2 {
		t.Fatalf("choices too short: %v", choices)
	}
	if choices[0] != "── CUSTOM ──" {
		t.Errorf("choices[0] = %q, want \"── CUSTOM ──\"", choices[0])
	}
	if choices[1] != "custom (edit ports)" {
		t.Errorf("choices[1] = %q, want \"custom (edit ports)\"", choices[1])
	}
}
