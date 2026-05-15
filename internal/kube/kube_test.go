package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func newTestClient(objs ...runtime.Object) *Client {
	return &Client{
		Clientset: fake.NewSimpleClientset(objs...),
		RawConfig: clientcmdapi.Config{},
	}
}

func TestGetPodRoleReadsSpiloLabel(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg-0",
			Namespace: "billing",
			Labels:    map[string]string{"spilo-role": "master"},
		},
	}
	c := newTestClient(pod)
	role, err := c.GetPodRole(context.Background(), "billing", "pg-0")
	if err != nil {
		t.Fatal(err)
	}
	if role != "master" {
		t.Errorf("role = %q, want master", role)
	}
}

func TestGetPodRoleReturnsUnknownWhenLabelMissing(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
	}
	c := newTestClient(pod)
	role, err := c.GetPodRole(context.Background(), "default", "pod-1")
	if err != nil {
		t.Fatal(err)
	}
	if role != "unknown" {
		t.Errorf("role = %q, want unknown", role)
	}
}

func TestGetPodsCountsRestartsAcrossContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{RestartCount: 2},
				{RestartCount: 3},
			},
		},
	}
	c := newTestClient(pod)
	pods, err := c.GetPods(context.Background(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 {
		t.Fatalf("len = %d, want 1", len(pods))
	}
	if pods[0].Restarts != 5 {
		t.Errorf("Restarts = %d, want 5", pods[0].Restarts)
	}
	if pods[0].Status != "Running" {
		t.Errorf("Status = %q, want Running", pods[0].Status)
	}
}

func TestGetPodsEmptyNamespace(t *testing.T) {
	c := newTestClient()
	pods, err := c.GetPods(context.Background(), "empty-ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 0 {
		t.Errorf("expected 0 pods in empty namespace, got %d", len(pods))
	}
}

func TestGetNamespaces(t *testing.T) {
	ns1 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}}
	ns2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "beta"}}
	c := newTestClient(ns1, ns2)
	names, err := c.GetNamespaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(names))
	}
}

func TestGetContexts(t *testing.T) {
	c := &Client{
		Clientset: fake.NewSimpleClientset(),
		RawConfig: clientcmdapi.Config{
			Contexts: map[string]*clientcmdapi.Context{
				"ctx-a": {},
				"ctx-b": {},
			},
			CurrentContext: "ctx-a",
		},
	}
	ctxs := c.GetContexts()
	if len(ctxs) != 2 {
		t.Errorf("GetContexts = %v, want 2 entries", ctxs)
	}
	if c.GetCurrentContext() != "ctx-a" {
		t.Errorf("GetCurrentContext = %q, want ctx-a", c.GetCurrentContext())
	}
}

func TestGetPodPortsCollectsPorts(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "main",
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080},
						{Name: "grpc", ContainerPort: 9090},
					},
				},
			},
		},
	}
	c := newTestClient(pod)
	ports, err := c.GetPodPorts(context.Background(), "default", "svc-pod")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(ports))
	}
	if ports[0].Port != 8080 {
		t.Errorf("ports[0].Port = %d, want 8080", ports[0].Port)
	}
}
