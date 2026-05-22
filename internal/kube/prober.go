package kube

import (
	"context"
	"sort"

	"github.com/billygate/kap-toolsbox/internal/portfwd"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// labelBlacklist holds labels that are ephemeral or rollout-specific —
// they must be stripped before storing as the "selectable" labels used
// for re-resolution after a pod restart.
var labelBlacklist = map[string]bool{
	"pod-template-hash":                  true,
	"controller-revision-hash":           true,
	"statefulset.kubernetes.io/pod-name": true,
}

// ResolvePod looks up the pod and returns selectable labels (preferring
// the controlling owner's MatchLabels when available) plus UID. The
// returned label set is safe to feed back into FindReadyPodByLabels.
func (c *Client) ResolvePod(ctx context.Context, namespace, name string) (portfwd.PodRef, error) {
	pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return portfwd.PodRef{}, portfwd.ErrPodNotFound
		}
		return portfwd.PodRef{}, err
	}

	labels := c.ownerSelectorLabels(ctx, namespace, pod)
	if labels == nil {
		labels = filterBlacklist(pod.Labels)
	}

	return portfwd.PodRef{
		Name:   pod.Name,
		UID:    string(pod.UID),
		Phase:  string(pod.Status.Phase),
		Ready:  podReady(pod),
		Labels: labels,
	}, nil
}

// GetPod returns current pod state without the owner-label traversal that
// ResolvePod does. Used by the supervisor's liveness ticker.
func (c *Client) GetPod(ctx context.Context, namespace, name string) (portfwd.PodRef, error) {
	pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return portfwd.PodRef{}, portfwd.ErrPodNotFound
		}
		return portfwd.PodRef{}, err
	}
	return portfwd.PodRef{
		Name:  pod.Name,
		UID:   string(pod.UID),
		Phase: string(pod.Status.Phase),
		Ready: podReady(pod),
	}, nil
}

// FindReadyPodByLabels lists pods matching labels (sorted by name for
// determinism) and returns the first one whose phase is Running and
// which has the PodReady condition true. ErrPodNotFound when none match.
func (c *Client) FindReadyPodByLabels(ctx context.Context, namespace string, labels map[string]string) (string, error) {
	selector := metav1.LabelSelector{MatchLabels: labels}
	sel, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return "", err
	}
	list, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(list.Items))
	byName := map[string]corev1.Pod{}
	for _, p := range list.Items {
		names = append(names, p.Name)
		byName[p.Name] = p
	}
	sort.Strings(names)
	for _, n := range names {
		p := byName[n]
		if p.Status.Phase == corev1.PodRunning && podReady(&p) {
			return n, nil
		}
	}
	return "", portfwd.ErrPodNotFound
}

// ownerSelectorLabels walks one level up the OwnerReferences chain to
// retrieve the controller's MatchLabels. Returns nil if no controlling
// owner is found or the owner kind is not one we recognise.
func (c *Client) ownerSelectorLabels(ctx context.Context, namespace string, pod *corev1.Pod) map[string]string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		switch ref.Kind {
		case "ReplicaSet":
			rs, err := c.Clientset.AppsV1().ReplicaSets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil || rs.Spec.Selector == nil {
				continue
			}
			return cloneStringMap(rs.Spec.Selector.MatchLabels)
		case "StatefulSet":
			ss, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil || ss.Spec.Selector == nil {
				continue
			}
			return cloneStringMap(ss.Spec.Selector.MatchLabels)
		case "DaemonSet":
			ds, err := c.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil || ds.Spec.Selector == nil {
				continue
			}
			return cloneStringMap(ds.Spec.Selector.MatchLabels)
		}
	}
	return nil
}

func filterBlacklist(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if labelBlacklist[k] {
			continue
		}
		out[k] = v
	}
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Compile-time check that *Client satisfies portfwd.Prober.
var _ portfwd.Prober = (*Client)(nil)
