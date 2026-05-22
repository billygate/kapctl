package portfwd

import (
	"context"
	"errors"
)

// PodRef is the subset of pod state the supervisor needs.
type PodRef struct {
	Name   string
	UID    string
	Phase  string            // "Running", "Pending", ...
	Ready  bool              // populated by ResolvePod/GetPod; the supervisor's liveness check uses Phase, not Ready
	Labels map[string]string // selectable labels (no pod-template-hash etc.)
}

// Prober is the small slice of k8s API operations the supervisor uses
// for pod resolution and liveness checks. The real implementation lives
// in internal/kube; tests substitute a fake.
type Prober interface {
	// ResolvePod returns the pod's selectable labels (owner-selector labels,
	// not raw pod labels) and UID. Returns ErrPodNotFound if the pod does
	// not exist; other errors are transient API failures. Called once at
	// Start to capture the inputs needed to re-resolve the pod after a
	// restart.
	ResolvePod(ctx context.Context, namespace, name string) (PodRef, error)

	// GetPod returns current state of the named pod. When the pod is
	// absent from the cluster, returns ErrPodNotFound (use errors.Is).
	GetPod(ctx context.Context, namespace, name string) (PodRef, error)

	// FindReadyPodByLabels returns the name of any Ready pod matching
	// labels in namespace, sorted by name for determinism. Returns
	// ErrPodNotFound when no candidate is Ready.
	FindReadyPodByLabels(ctx context.Context, namespace string, labels map[string]string) (string, error)
}

// ProberFactory builds a Prober for a given kube context. Each forward
// may target a different context, so the factory is invoked once per
// Start with the entry's context name.
type ProberFactory func(contextName string) (Prober, error)

// ErrPodNotFound is returned by Prober when the target pod is absent
// from the cluster. The supervisor treats this as a reconnect trigger
// rather than a fatal Start error (after the initial resolution).
var ErrPodNotFound = errors.New("portfwd: pod not found")
