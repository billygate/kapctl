// Package kube wraps k8s.io/client-go with the small surface kap's
// TUI and CLI consume: contexts, namespaces, pods, port specs, and
// the Patroni/Spilo role label.
package kube

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	// Register all client-go auth plugins (gcp, oidc, exec, etc.) so
	// kubeconfigs that use them work without extra setup.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Client is the kapctl-facing wrapper around a Kubernetes clientset plus
// the raw kubeconfig (used to enumerate contexts).
type Client struct {
	Clientset kubernetes.Interface
	RawConfig clientcmdapi.Config
}

// NewClient builds a Client from the default kubeconfig loading rules.
// Pass an empty contextName to use the kubeconfig's current-context.
func NewClient(contextName string) (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		configOverrides.CurrentContext = contextName
	}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	// Set a reasonable timeout for all requests
	config.Timeout = 120 * time.Second

	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		Clientset: clientset,
		RawConfig: rawConfig,
	}, nil
}

// GetContexts returns every context name from the merged kubeconfig.
func (c *Client) GetContexts() []string {
	var names []string
	for name := range c.RawConfig.Contexts {
		names = append(names, name)
	}
	return names
}

// GetCurrentContext returns the kubeconfig's current-context name.
func (c *Client) GetCurrentContext() string {
	return c.RawConfig.CurrentContext
}

// GetNamespaces lists every namespace name in the cluster.
func (c *Client) GetNamespaces(ctx context.Context) ([]string, error) {
	nsList, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var names []string
	for _, ns := range nsList.Items {
		names = append(names, ns.Name)
	}
	return names, nil
}

// GetPods returns a PodInfo summary for every pod in the namespace.
func (c *Client) GetPods(ctx context.Context, namespace string) ([]PodInfo, error) {
	podList, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var pods []PodInfo
	for _, pod := range podList.Items {
		restarts := int32(0)
		for _, cs := range pod.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}

		var ports []int32
		for _, container := range pod.Spec.Containers {
			for _, p := range container.Ports {
				ports = append(ports, p.ContainerPort)
			}
		}

		pods = append(pods, PodInfo{
			Name:     pod.Name,
			Status:   string(pod.Status.Phase),
			Restarts: restarts,
			Age:      time.Since(pod.CreationTimestamp.Time).Round(time.Second).String(),
			Ports:    ports,
		})
	}
	return pods, nil
}

// GetPodRole returns the value of the spilo-role label (Patroni/Spilo
// Postgres operator). Pods without the label report "unknown".
func (c *Client) GetPodRole(ctx context.Context, namespace, podName string) (string, error) {
	pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	role, ok := pod.Labels["spilo-role"]
	if !ok {
		return "unknown", nil
	}
	return role, nil
}

// DeletePod deletes the named pod with the default graceful-termination
// period. For pods owned by a controller (Deployment, StatefulSet, …)
// the controller will recreate the pod.
func (c *Client) DeletePod(ctx context.Context, namespace, podName string) error {
	return c.Clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
}

// GetPodPorts returns every container port declared on the pod spec.
func (c *Client) GetPodPorts(ctx context.Context, namespace, podName string) ([]ContainerPort, error) {
	pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	var ports []ContainerPort
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			ports = append(ports, ContainerPort{
				Name: port.Name,
				Port: port.ContainerPort,
			})
		}
	}
	return ports, nil
}
