package overlays

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/billygate/kapctl/internal/kube"
	"github.com/billygate/kapctl/internal/tui/styles"
)

const customPortLabel = "custom (edit ports)"

// IsCustomPortChoice reports whether the picker label is the CUSTOM
// sentinel. ParsePort errors on this string by design — callers must
// branch on IsCustomPortChoice first.
func IsCustomPortChoice(label string) bool {
	return strings.TrimSpace(label) == customPortLabel
}

// ParsePort extracts the leading integer from a label like "5432 (postgresql)".
// Returns an error if the label has no leading integer.
func ParsePort(label string) (int, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return 0, fmt.Errorf("empty port label")
	}
	fields := strings.Fields(label)
	if len(fields) == 0 {
		return 0, fmt.Errorf("no fields in %q", label)
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("port label %q: leading field is not a number: %w", label, err)
	}
	return n, nil
}

// BuildPortChoices returns the labelled menu items for the port-selection
// overlay, in display order (priority -> detected -> common). Detected and
// common ports are deduplicated against priority.
func BuildPortChoices(detected []kube.ContainerPort) []string {
	priority := []struct {
		Port int
		Desc string
	}{
		{5432, "postgresql"},
		{8080, "http-alt"},
	}
	commonOrdered := []struct {
		Port int
		Desc string
	}{
		{80, "http"},
		{6379, "redis"},
		{9090, "prometheus"},
		{3000, "grafana"},
	}

	seen := map[int]bool{}
	var out []string

	out = append(out, "── CUSTOM ──")
	out = append(out, customPortLabel)
	out = append(out, "── PRIORITY ──")
	for _, p := range priority {
		out = append(out, fmt.Sprintf("%d (%s)", p.Port, p.Desc))
		seen[p.Port] = true
	}

	if len(detected) > 0 {
		var detectedItems []string
		for _, p := range detected {
			if seen[int(p.Port)] {
				continue
			}
			label := strconv.Itoa(int(p.Port))
			if p.Name != "" {
				label = fmt.Sprintf("%d (%s)", p.Port, p.Name)
			}
			detectedItems = append(detectedItems, label)
			seen[int(p.Port)] = true
		}
		if len(detectedItems) > 0 {
			out = append(out, "── DETECTED ──")
			out = append(out, detectedItems...)
		}
	}

	out = append(out, "── COMMON ──")
	for _, p := range commonOrdered {
		if seen[p.Port] {
			continue
		}
		out = append(out, fmt.Sprintf("%d (%s)", p.Port, p.Desc))
		seen[p.Port] = true
	}
	return out
}

// PickPort runs an interactive port picker and returns the parsed port.
// Used by panes/explorer.go for the port-forward action and by the pgsql
// Cobra subcommand. Returns 0 with no error if the user cancelled the picker.
func PickPort(detected []kube.ContainerPort, s *styles.Styles) (int, error) {
	choices := BuildPortChoices(detected)
	picked, err := Pick("Select port", choices, s)
	if err != nil {
		return 0, err
	}
	if picked == "" {
		return 0, nil
	}
	// Skip separator lines if somehow returned (Pick should already prevent this)
	if strings.HasPrefix(picked, "──") {
		return 0, nil
	}
	return ParsePort(picked)
}
