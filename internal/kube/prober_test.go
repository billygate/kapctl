package kube

import (
	"context"
	"errors"
	"testing"

	"github.com/billygate/kap-toolsbox/internal/portfwd"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestResolvePodUsesOwnerSelectorLabels(t *testing.T) {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-rs",
			Namespace: "ns",
			UID:       "rs-uid",
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "demo"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-pod",
			Namespace: "ns",
			UID:       "pod-uid",
			Labels: map[string]string{
				"app":               "demo",
				"pod-template-hash": "abc123",
			},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "demo-rs", UID: types.UID("rs-uid"), Controller: ptrBool(true)},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	c := newTestClient(pod, rs)
	ref, err := c.ResolvePod(context.Background(), "ns", "demo-pod")
	if err != nil {
		t.Fatalf("ResolvePod: %v", err)
	}
	if ref.Labels["app"] != "demo" {
		t.Errorf("missing app=demo: %+v", ref.Labels)
	}
	if _, ok := ref.Labels["pod-template-hash"]; ok {
		t.Errorf("pod-template-hash should be stripped: %+v", ref.Labels)
	}
	if ref.UID != "pod-uid" {
		t.Errorf("UID = %q, want pod-uid", ref.UID)
	}
}

func TestResolvePodStripsHashFromReplicaSetSelector(t *testing.T) {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-rs-abc123",
			Namespace: "ns",
			UID:       "rs-uid",
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":               "demo",
					"pod-template-hash": "abc123",
				},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-pod",
			Namespace: "ns",
			UID:       "pod-uid",
			Labels: map[string]string{
				"app":               "demo",
				"pod-template-hash": "abc123",
			},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "demo-rs-abc123", UID: types.UID("rs-uid"), Controller: ptrBool(true)},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	c := newTestClient(pod, rs)
	ref, err := c.ResolvePod(context.Background(), "ns", "demo-pod")
	if err != nil {
		t.Fatalf("ResolvePod: %v", err)
	}
	if _, ok := ref.Labels["pod-template-hash"]; ok {
		t.Errorf("RS selector pod-template-hash should be stripped: %+v", ref.Labels)
	}
	if ref.Labels["app"] != "demo" {
		t.Errorf("missing app=demo: %+v", ref.Labels)
	}
}

func TestResolvePodBarePodUsesFilteredLabels(t *testing.T) {
	// Pod with no OwnerReferences — fall back to pod.Labels minus blacklist.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bare",
			Namespace: "ns",
			UID:       "bare-uid",
			Labels: map[string]string{
				"app":                      "demo",
				"pod-template-hash":        "ignored",
				"controller-revision-hash": "also-ignored",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := newTestClient(pod)
	ref, err := c.ResolvePod(context.Background(), "ns", "bare")
	if err != nil {
		t.Fatalf("ResolvePod: %v", err)
	}
	if ref.Labels["app"] != "demo" {
		t.Errorf("missing app=demo: %+v", ref.Labels)
	}
	if _, ok := ref.Labels["pod-template-hash"]; ok {
		t.Errorf("pod-template-hash should be stripped: %+v", ref.Labels)
	}
	if _, ok := ref.Labels["controller-revision-hash"]; ok {
		t.Errorf("controller-revision-hash should be stripped: %+v", ref.Labels)
	}
}

func TestGetPodReturnsErrPodNotFound(t *testing.T) {
	c := newTestClient()
	_, err := c.GetPod(context.Background(), "ns", "nope")
	if !errors.Is(err, portfwd.ErrPodNotFound) {
		t.Errorf("err = %v, want ErrPodNotFound", err)
	}
}

func TestResolvePodReturnsErrPodNotFound(t *testing.T) {
	c := newTestClient()
	_, err := c.ResolvePod(context.Background(), "ns", "nope")
	if !errors.Is(err, portfwd.ErrPodNotFound) {
		t.Errorf("err = %v, want ErrPodNotFound", err)
	}
}

func TestFindReadyPodByLabelsPicksFirstReady(t *testing.T) {
	notReady := makePod("pg-0", "ns", map[string]string{"app": "pg"}, corev1.PodRunning, false)
	ready := makePod("pg-1", "ns", map[string]string{"app": "pg"}, corev1.PodRunning, true)
	c := newTestClient(notReady, ready)
	name, err := c.FindReadyPodByLabels(context.Background(), "ns", map[string]string{"app": "pg"})
	if err != nil {
		t.Fatalf("FindReadyPodByLabels: %v", err)
	}
	if name != "pg-1" {
		t.Errorf("name = %q, want pg-1", name)
	}
}

func TestFindReadyPodByLabelsErrPodNotFoundWhenNoneReady(t *testing.T) {
	notReady := makePod("pg-0", "ns", map[string]string{"app": "pg"}, corev1.PodRunning, false)
	c := newTestClient(notReady)
	_, err := c.FindReadyPodByLabels(context.Background(), "ns", map[string]string{"app": "pg"})
	if !errors.Is(err, portfwd.ErrPodNotFound) {
		t.Errorf("err = %v, want ErrPodNotFound", err)
	}
}

func ptrBool(b bool) *bool { return &b }

func makePod(name, ns string, labels map[string]string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	conds := []corev1.PodCondition{}
	if ready {
		conds = append(conds, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Phase:      phase,
			Conditions: conds,
		},
	}
}
