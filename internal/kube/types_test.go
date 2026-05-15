package kube

import "testing"

func TestHasContainerPort(t *testing.T) {
	ports := []ContainerPort{
		{Name: "http", Port: 8080},
		{Name: "metrics", Port: 9090},
	}
	cases := []struct {
		name string
		p    int32
		want bool
	}{
		{"present-first", 8080, true},
		{"present-second", 9090, true},
		{"absent", 1234, false},
		{"zero", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasContainerPort(ports, c.p); got != c.want {
				t.Errorf("HasContainerPort(_, %d) = %v, want %v", c.p, got, c.want)
			}
		})
	}
	if HasContainerPort(nil, 80) {
		t.Error("HasContainerPort(nil, 80) = true, want false")
	}
}
