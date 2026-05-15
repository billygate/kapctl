// Package main is the kapctl CLI entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/billygate/kap-toolsbox/internal/kube"
	"github.com/billygate/kap-toolsbox/internal/tui/overlays"
	"github.com/spf13/cobra"
)

var ctrlCmd = &cobra.Command{
	Use:   "ctrl",
	Short: "interactive kubectl helper",
	RunE: func(_ *cobra.Command, _ []string) error {
		k, err := kube.NewClient("")
		if err != nil {
			return err
		}

		ctxNames := k.GetContexts()
		pickedCtx, err := overlays.Pick("Select context", ctxNames, loadedStyles)
		if err != nil || pickedCtx == "" {
			return nil
		}

		// Re-initialize client with selected context
		k, err = kube.NewClient(pickedCtx)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", loadedStyles.Muted.Render("Context:"), loadedStyles.Value.Render(pickedCtx))

		nsNames, err := k.GetNamespaces(context.Background())
		if err != nil {
			fmt.Printf("%s %v\n", loadedStyles.Warn.Render("Error:"), err)
			fmt.Println(loadedStyles.Muted.Render("(check VPN / SSO / auth - try: kubectl get ns --context " + pickedCtx + ")"))
			return nil
		}
		pickedNS, err := overlays.Pick("Select namespace", nsNames, loadedStyles)
		if err != nil || pickedNS == "" {
			return nil
		}
		fmt.Printf("%s %s\n", loadedStyles.Muted.Render("Namespace:"), loadedStyles.Value.Render(pickedNS))

		pods, err := k.GetPods(context.Background(), pickedNS)
		if err != nil {
			return err
		}

		var podNames []string
		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
		pickedPod, err := overlays.Pick("Select pod", podNames, loadedStyles)
		if err != nil || pickedPod == "" {
			return nil
		}

		actions := []string{"logs", "exec", "describe", "delete"}
		pickedAction, err := overlays.Pick("Action", actions, loadedStyles)
		if err != nil || pickedAction == "" {
			return nil
		}

		if pickedAction == "delete" {
			if err := k.DeletePod(context.Background(), pickedNS, pickedPod); err != nil {
				fmt.Printf("%s %v\n", loadedStyles.Warn.Render("Error:"), err)
				return nil
			}
			fmt.Printf("%s %s\n", loadedStyles.Muted.Render("Deleted pod:"), loadedStyles.Value.Render(pickedPod))
			return nil
		}

		return runAction(pickedCtx, pickedNS, pickedPod, pickedAction)
	},
}

func runAction(ctx, ns, pod, action string) error {
	var cmdArgs []string
	switch action {
	case "logs":
		cmdArgs = []string{"--context", ctx, "-n", ns, "logs", "-f", pod}
	case "exec":
		cmdArgs = []string{"--context", ctx, "-n", ns, "exec", "-it", pod, "--", "sh"}
	case "describe":
		cmdArgs = []string{"--context", ctx, "-n", ns, "describe", "pod", pod}
	}

	c := exec.Command("kubectl", cmdArgs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

func init() {
	rootCmd.AddCommand(ctrlCmd)
}
