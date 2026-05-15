package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/billygate/kap-toolsbox/internal/kube"
	"github.com/billygate/kap-toolsbox/internal/tui/overlays"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var pgsqlCmd = &cobra.Command{
	Use:   "pgsql",
	Short: "PostgreSQL port-forwarding helper",
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
			return err
		}
		pickedNS, err := overlays.Pick("Select namespace", nsNames, loadedStyles)
		if err != nil || pickedNS == "" {
			return nil
		}

		pods, err := k.GetPods(context.Background(), pickedNS)
		if err != nil {
			return err
		}

		var podNames []string
		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
		pickedPod, err := overlays.Pick("Select PostgreSQL pod", podNames, loadedStyles)
		if err != nil || pickedPod == "" {
			return nil
		}

		role, err := k.GetPodRole(context.Background(), pickedNS, pickedPod)
		if err != nil {
			role = "unknown"
		}

		roleColor := loadedStyles.Palette.SpiloRole(role)
		colorRole := lipgloss.NewStyle().Foreground(roleColor).Render(role)
		fmt.Printf("%s %s (%s)\n", loadedStyles.Muted.Render("Pod:"), loadedStyles.Value.Render(pickedPod), colorRole)

		defaultPort := loadedConfig.GetPort(pickedCtx, pickedNS)
		if defaultPort == 0 {
			defaultPort = 5432
		}
		localPort := defaultPort
		fmt.Printf("%s %d\n", loadedStyles.Muted.Render("Port:"), localPort)
		loadedConfig.SetPort(pickedCtx, pickedNS, localPort)
		if err := loadedConfig.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "config save: %v\n", err)
		}

		c := exec.Command("kubectl", "--context", pickedCtx, "-n", pickedNS, "port-forward", pickedPod, fmt.Sprintf("%d:5432", localPort))
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(pgsqlCmd)
}
