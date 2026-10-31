package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/node"
)

func newPairCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Link another device to this account",
		Long:  "Pair a second device without copying any private keys. Run `toget pair create` on the device that already has the account, then `toget pair join <code>` on the new device and approve it here.",
	}
	cmd.AddCommand(newPairCreateCommand())
	cmd.AddCommand(newPairListCommand())
	cmd.AddCommand(newPairApproveCommand())
	cmd.AddCommand(newPairCancelCommand())
	cmd.AddCommand(newPairJoinCommand())
	cmd.AddCommand(newPairJoinStatusCommand())
	return cmd
}

func newPairCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create",
		Short: "Create a pairing code for a new device",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var view node.PairingView
			if err := c.Post(cmd.Context(), "/api/v1/pairings", map[string]any{}, &view); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(view)
			}
			fmt.Println("Pairing code (valid for 10 minutes):")
			fmt.Println()
			fmt.Println(view.Code)
			fmt.Println()
			fmt.Println("On the new device run `toget pair join` and paste this code,")
			fmt.Println("then approve it here with `toget pair approve " + view.ID + "`.")
			return nil
		},
	}
}

func newPairListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List pairing sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Pairings []node.PairingView `json:"pairings"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/pairings", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Pairings)
			}
			if len(result.Pairings) == 0 {
				fmt.Println("No pairing sessions.")
				return nil
			}
			for _, p := range result.Pairings {
				label := p.DeviceName
				if label == "" {
					label = "(waiting for a device)"
				}
				fmt.Printf("%s  %-16s  %s  expires %s\n", p.ID, p.Status, label, p.ExpiresAt.Local().Format("15:04"))
				if p.Fingerprint != "" {
					fmt.Printf("  fingerprint: %s\n", p.Fingerprint)
				}
			}
			return nil
		},
	}
}

func newPairApproveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <pairing-id>",
		Short: "Approve a device that submitted a pairing request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var view node.PairingView
			if err := c.Post(cmd.Context(), "/api/v1/pairings/"+args[0]+"/approve", map[string]any{}, &view); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(view)
			}
			fmt.Printf("Approved %s. The new device is importing the account now.\n", view.DeviceName)
			for _, warning := range view.Warnings {
				fmt.Println("Note:", warning)
			}
			return nil
		},
	}
}

func newPairCancelCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <pairing-id>",
		Short: "Cancel a pairing session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/api/v1/pairings/"+args[0], nil); err != nil {
				return err
			}
			fmt.Println("Pairing canceled.")
			return nil
		},
	}
}

func newPairJoinCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "join [pairing-code]",
		Short: "Recover this account from a pairing code (new device)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code := ""
			if len(args) == 1 {
				code = args[0]
			} else {
				fmt.Println("Paste the pairing code from the other device, then press Enter:")
				line, err := stdinReader.ReadString('\n')
				if err != nil && strings.TrimSpace(line) == "" {
					return fmt.Errorf("read pairing code: %w", err)
				}
				code = strings.TrimSpace(line)
			}
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var view node.PairJoinView
			payload := map[string]string{"code": code, "device_name": name}
			if err := c.Post(cmd.Context(), "/api/v1/pairing/join", payload, &view); err != nil {
				if strings.Contains(err.Error(), account.ErrAlreadyExists.Error()) {
					return fmt.Errorf("this device already has an account; pairing only works on a fresh device")
				}
				return err
			}
			if jsonFlag {
				return printJSON(view)
			}
			fmt.Println("Request sent.")
			fmt.Printf("This device's fingerprint: %s\n", view.Fingerprint)
			fmt.Println("Compare it with the approval prompt on the other device before approving. Waiting…")
			for {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(2 * time.Second):
				}
				var status node.PairJoinView
				if err := c.Get(cmd.Context(), "/api/v1/pairing/join/"+view.ID, &status); err != nil {
					return err
				}
				switch status.Status {
				case "ready":
					fmt.Printf("Done. This device is now %s on the account.\n", status.DeviceName)
					for _, warning := range status.Warnings {
						fmt.Println("Note:", warning)
					}
					return nil
				case "failed", "canceled", "expired":
					message := "Pairing " + status.Status + "."
					if status.Error != "" {
						message += " " + status.Error
					}
					return fmt.Errorf("%s", message)
				default:
					fmt.Println("  … " + status.Status)
				}
			}
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name for this device")
	return cmd
}

func newPairJoinStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status <join-id>",
		Short: "Show the status of a pairing on this device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var view node.PairJoinView
			if err := c.Get(cmd.Context(), "/api/v1/pairing/join/"+args[0], &view); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(view)
			}
			fmt.Printf("%s  %s\n", view.Status, view.DeviceName)
			if view.Error != "" {
				fmt.Println(view.Error)
			}
			return nil
		},
	}
}
