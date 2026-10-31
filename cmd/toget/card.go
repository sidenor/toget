package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/qr"
)

func newCardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "card",
		Short: "Show and manage portable server cards",
		Long:  "A card carries a server's signed identity, endpoint, and certificate pin. It can be copied as text or rendered as a QR code and imported on another node without a directory.",
	}
	cmd.AddCommand(newCardShowCommand())
	cmd.AddCommand(newCardAddCommand())
	cmd.AddCommand(newCardListCommand())
	cmd.AddCommand(newCardRemoveCommand())
	return cmd
}

func newCardShowCommand() *cobra.Command {
	var label string
	var qrFlag bool
	var pngPath string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show this node's business card",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			query := url.Values{}
			if label != "" {
				query.Set("label", label)
			}
			path := "/api/v1/card"
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}
			var result struct {
				Card node.CardView `json:"card"`
			}
			if err := c.Get(cmd.Context(), path, &result); err != nil {
				return err
			}
			view := result.Card
			if jsonFlag {
				return printJSON(view)
			}
			fmt.Printf("Label:      %s\n", displayCardLabel(view.Label))
			fmt.Printf("Server:     %s\n", shortServerID(view.ServerID))
			fmt.Printf("Code:       %s\n", view.ServerCode)
			fmt.Printf("Account:    %s\n", formatAccountID(view.AccountID))
			fmt.Printf("Identity:   expires %s\n", view.IdentityExpiresAt.Local().Format(time.RFC3339))
			fmt.Printf("Node:       expires %s\n", view.NodeExpiresAt.Local().Format(time.RFC3339))
			fmt.Printf("\n%s\n", view.Card)
			if qrFlag {
				text, err := qr.Text(view.Card)
				if err != nil {
					return err
				}
				fmt.Print("\n" + text)
			}
			if pngPath != "" {
				query.Set("scale", "10")
				data, _, err := c.Download(cmd.Context(), "/api/v1/card/qr?"+query.Encode())
				if err != nil {
					return err
				}
				if err := os.WriteFile(pngPath, data, 0o644); err != nil {
					return err
				}
				fmt.Printf("\nWrote QR code to %s\n", pngPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "card label (defaults to the account name)")
	cmd.Flags().BoolVar(&qrFlag, "qr", false, "print a QR code in the terminal")
	cmd.Flags().StringVar(&pngPath, "png", "", "write a PNG QR code to this file")
	return cmd
}

func newCardAddCommand() *cobra.Command {
	var label string
	var file string
	cmd := &cobra.Command{
		Use:   "add [card]",
		Short: "Verify and save a business card",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var raw string
			switch {
			case file != "":
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				raw = string(data)
			case len(args) == 1:
				raw = args[0]
			default:
				fmt.Fprintln(os.Stderr, "Paste a Toget card, then press Ctrl-D:")
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				raw = string(data)
			}
			if strings.TrimSpace(raw) == "" {
				return errors.New("no card was provided")
			}
			var result struct {
				Card node.CardView `json:"card"`
			}
			if err := c.Post(cmd.Context(), "/api/v1/cards", map[string]string{
				"card": raw, "label": label,
			}, &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Card)
			}
			fmt.Printf("Saved %s (%s · account %s)\n",
				displayCardLabel(result.Card.Label), result.Card.ServerCode,
				formatAccountID(result.Card.AccountID))
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "local label for this card")
	cmd.Flags().StringVar(&file, "file", "", "read the card from a file")
	return cmd
}

func newCardListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved business cards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			var result struct {
				Cards []node.CardView `json:"cards"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/cards", &result); err != nil {
				return err
			}
			if jsonFlag {
				return printJSON(result.Cards)
			}
			if len(result.Cards) == 0 {
				fmt.Println("No saved cards.")
				return nil
			}
			for _, view := range result.Cards {
				fmt.Printf("%s  %s  %s  expires %s\n",
					displayCardLabel(view.Label), view.ServerCode,
					shortServerID(view.ServerID), view.IdentityExpiresAt.Local().Format("2006-01-02"))
			}
			return nil
		},
	}
}

func newCardRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <server-id>",
		Short: "Remove a saved business card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, err := localClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/api/v1/cards/"+url.PathEscape(args[0]), nil); err != nil {
				return err
			}
			fmt.Println("Card removed.")
			return nil
		},
	}
}

func shortServerID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:20] + "…"
}

func displayCardLabel(label string) string {
	if strings.TrimSpace(label) == "" {
		return "(no label)"
	}
	return label
}
