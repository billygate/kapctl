package kube

// PodInfo is the projection of a Pod used by kap's TUI.
type PodInfo struct {
	Name     string
	Status   string
	Restarts int32
	Age      string
	Ports    []int32
}

// ContainerPort is a (name, port) pair declared in a pod's container spec.
type ContainerPort struct {
	Name string
	Port int32
}

// HasContainerPort reports whether ports declares the given port number.
func HasContainerPort(ports []ContainerPort, port int32) bool {
	for _, p := range ports {
		if p.Port == port {
			return true
		}
	}
	return false
}
