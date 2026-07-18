package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/billygate/kapctl/internal/kube"
	"github.com/billygate/kapctl/internal/portfwd"
	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/spf13/cobra"
)

var (
	fwdContext string
	fwdNS      string
	fwdPod     string
	fwdPort    string
)

var fwdCmd = &cobra.Command{
	Use:   "fwd",
	Short: "port-forward a pod (foreground, auto-reconnecting)",
	Long: `Forward a local port to a pod through kubectl, supervised with the same
auto-reconnect and health-probing the FORWARDS tab uses. Runs in the
foreground until Ctrl-C.

Any of context/namespace/pod/port omitted via flags falls back to an
interactive picker.`,
	RunE: runFwd,
}

func runFwd(_ *cobra.Command, _ []string) error {
	k, err := kube.NewClient("")
	if err != nil {
		return err
	}

	pickedCtx := fwdContext
	if pickedCtx == "" {
		pickedCtx, err = overlays.Pick("Select context", k.GetContexts(), loadedStyles)
		if err != nil || pickedCtx == "" {
			return err
		}
	}
	if k, err = kube.NewClient(pickedCtx); err != nil {
		return err
	}

	pickedNS := fwdNS
	if pickedNS == "" {
		nsNames, err := k.GetNamespaces(context.Background())
		if err != nil {
			return err
		}
		if pickedNS, err = overlays.Pick("Select namespace", nsNames, loadedStyles); err != nil || pickedNS == "" {
			return err
		}
	}

	pickedPod := fwdPod
	if pickedPod == "" {
		pods, err := k.GetPods(context.Background(), pickedNS)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(pods))
		for _, p := range pods {
			names = append(names, p.Name)
		}
		if pickedPod, err = overlays.Pick("Select pod", names, loadedStyles); err != nil || pickedPod == "" {
			return err
		}
	}

	localPort, remotePort, err := resolvePorts(k, pickedNS, pickedPod)
	if err != nil {
		return err
	}

	if err := portfwd.IsLocalPortFree(localPort); err != nil {
		return fmt.Errorf("local port %d is not free (%w); choose another with -p local:remote", localPort, err)
	}

	mgr := portfwd.NewManager(0, 0)
	mgr.SetProberFactory(func(contextName string) (portfwd.Prober, error) {
		return kube.NewClient(contextName)
	})
	id, err := mgr.Start(portfwd.StartOpts{
		Context:    pickedCtx,
		Namespace:  pickedNS,
		Target:     pickedPod,
		Kind:       portfwd.KindPod,
		LocalPort:  localPort,
		RemotePort: remotePort,
	})
	if err != nil {
		return err
	}

	fmt.Printf("%s localhost:%d → %s/%s:%d (%s)\n",
		loadedStyles.Muted.Render("Forwarding"),
		localPort,
		loadedStyles.Value.Render(pickedNS),
		loadedStyles.Value.Render(pickedPod),
		remotePort,
		loadedStyles.Muted.Render(pickedCtx))
	fmt.Println(loadedStyles.Muted.Render("Press Ctrl-C to stop."))

	return watchForward(mgr, id)
}

// resolvePorts returns the local and remote ports, either from the --port
// flag or via an interactive picker seeded with the pod's declared ports.
func resolvePorts(k *kube.Client, ns, pod string) (local, remote int, err error) {
	if fwdPort != "" {
		return parsePortSpec(fwdPort)
	}
	detected, _ := k.GetPodPorts(context.Background(), ns, pod)
	label, err := overlays.Pick("Select port", overlays.BuildPortChoices(detected), loadedStyles)
	if err != nil || label == "" {
		return 0, 0, err
	}
	remote, err = overlays.ParsePort(label)
	if err != nil {
		return 0, 0, err
	}
	return remote, remote, nil
}

// watchForward blocks streaming the forward's status transitions until the
// user interrupts (SIGINT/SIGTERM) or the forward errors out for good.
func watchForward(mgr *portfwd.Manager, id string) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-sigCh:
			mgr.StopAll(3 * time.Second)
			fmt.Println(loadedStyles.Muted.Render("stopped"))
			return nil
		case ev := <-mgr.Events():
			if ev.ID != id {
				continue
			}
			printFwdEvent(ev)
			if ev.Status == portfwd.StatusErrored {
				mgr.StopAll(3 * time.Second)
				return fmt.Errorf("port-forward failed: %s", ev.Detail)
			}
		}
	}
}

func printFwdEvent(ev portfwd.Event) {
	marker := loadedStyles.Muted.Render("•")
	switch ev.Status {
	case portfwd.StatusRunning:
		marker = loadedStyles.Master.Render("●")
	case portfwd.StatusErrored:
		marker = loadedStyles.Warn.Render("✗")
	}
	line := ev.Status.String()
	if ev.Detail != "" {
		line += ": " + ev.Detail
	}
	fmt.Printf("  %s %s\n", marker, loadedStyles.Muted.Render(line))
}

// parsePortSpec parses a port flag of the form "port" (local==remote) or
// "local:remote". Ports must be in the range 1-65535.
func parsePortSpec(s string) (local, remote int, err error) {
	one := func(p string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0, fmt.Errorf("invalid port %q", p)
		}
		if n < 1 || n > 65535 {
			return 0, fmt.Errorf("port %d out of range 1-65535", n)
		}
		return n, nil
	}
	switch parts := strings.Split(s, ":"); len(parts) {
	case 1:
		n, err := one(parts[0])
		if err != nil {
			return 0, 0, err
		}
		return n, n, nil
	case 2:
		l, err := one(parts[0])
		if err != nil {
			return 0, 0, err
		}
		r, err := one(parts[1])
		if err != nil {
			return 0, 0, err
		}
		return l, r, nil
	default:
		return 0, 0, errors.New("port must be 'port' or 'local:remote'")
	}
}

func init() {
	fwdCmd.Flags().StringVarP(&fwdContext, "context", "c", "", "kube context (skips picker)")
	fwdCmd.Flags().StringVarP(&fwdNS, "namespace", "n", "", "namespace (skips picker)")
	fwdCmd.Flags().StringVar(&fwdPod, "pod", "", "pod name (skips picker)")
	fwdCmd.Flags().StringVarP(&fwdPort, "port", "p", "", "port as 'remote' or 'local:remote' (skips picker)")
	rootCmd.AddCommand(fwdCmd)
}
