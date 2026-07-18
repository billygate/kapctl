package kube

import (
	"context"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// LogOptions controls a GetPodLogs request. TailLines <= 0 means "no cap".
type LogOptions struct {
	Container string
	TailLines int
}

// GetPodLogs streams the named pod's logs and returns them as a string,
// capped to the last opts.TailLines lines. Container selects a specific
// container; empty means the pod's default container.
func (c *Client) GetPodLogs(ctx context.Context, namespace, pod string, opts LogOptions) (string, error) {
	logOpts := &corev1.PodLogOptions{}
	if opts.Container != "" {
		logOpts.Container = opts.Container
	}
	if opts.TailLines > 0 {
		tl := int64(opts.TailLines)
		logOpts.TailLines = &tl
	}

	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, logOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return capLines(string(data), opts.TailLines), nil
}

// capLines returns the last n non-trailing lines of s. n <= 0 is identity.
func capLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
