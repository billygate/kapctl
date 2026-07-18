package kube

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCapLines(t *testing.T) {
	in := "l1\nl2\nl3\nl4\n"
	if got := capLines(in, 2); got != "l3\nl4" {
		t.Fatalf("capLines last-2 = %q, want %q", got, "l3\nl4")
	}
	if got := capLines(in, 0); got != in {
		t.Fatalf("capLines n=0 should be identity, got %q", got)
	}
	if got := capLines("only", 5); got != "only" {
		t.Fatalf("capLines fewer-than-n = %q, want %q", got, "only")
	}
}

func TestGetPodLogs(t *testing.T) {
	cs := fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns"},
	})
	c := &Client{Clientset: cs}
	out, err := c.GetPodLogs(context.Background(), "ns", "p1", LogOptions{TailLines: 100})
	if err != nil {
		t.Fatalf("GetPodLogs error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("GetPodLogs returned empty output")
	}
}
