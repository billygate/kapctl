package docker

// ContainerStatus is the projection of a Docker container used by kap's TUI.
type ContainerStatus struct {
	Name   string
	Status string
	Age    string
}
