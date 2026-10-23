package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

func newLabelCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label <command>",
		Short: "Work with GitHub labels",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newLabelListCmd(s), newLabelCreateCmd(s))
	return cmd
}

// labelURL is the label's page, which the API doesn't return.
func labelURL(repo repoRef, name string) string {
	return "https://github.com/" + repo.String() + "/labels/" + url.PathEscape(name)
}

var colorRE = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

func newLabelCreateCmd(s IO) *cobra.Command {
	var color, description string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new label",
		Long: "Create a new label.\n\n" +
			"Without --color, GitHub picks the color. Prints the URL of the label.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().StringVarP(&color, "color", "c", "", "Color of the label, six hex digits")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Description of the label")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		name := args[0]
		if strings.TrimSpace(name) == "" {
			return errors.New("label name cannot be blank")
		}
		fields := record{"name": name}
		if color != "" {
			color = strings.TrimPrefix(color, "#")
			if !colorRE.MatchString(color) {
				return fmt.Errorf("invalid color %q: expected six hex digits, such as \"d73a4a\"", c.Flag("color").Value)
			}
			fields["color"] = color
		}
		if description != "" {
			fields["description"] = description
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		var l apiLabel
		if err := api.send(ctx, http.MethodPost, repo.ref.path("labels"), fields, &l); err != nil {
			return err
		}
		return writeURL(s.Out, labelURL(repo.ref, l.Name))
	}
	return cmd
}

func newLabelListCmd(s IO) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List labels in a repository",
		Args:  cobra.NoArgs,
	}
	repo := addRepoFlag(cmd)
	js := addJSONFlag(cmd, "color", "description", "isDefault", "name", "url")
	cmd.Flags().IntVarP(&limit, "limit", "L", 30, "Maximum number of labels to fetch")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if err := checkLimit(limit); err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		labels, more, err := list[apiLabel](ctx, api, repo.ref.path("labels"), nil, limit, nil)
		if err != nil {
			return err
		}
		if js.enabled() {
			rs := make([]record, len(labels))
			for i, l := range labels {
				rs[i] = record{
					"name":        l.Name,
					"description": l.Description,
					"color":       l.Color,
					"isDefault":   l.Default,
					"url":         labelURL(repo.ref, l.Name),
				}
			}
			if err := js.writeRecords(s.Out, rs); err != nil {
				return err
			}
		} else {
			rows := make([][]string, len(labels))
			for i, l := range labels {
				rows[i] = []string{l.Name, l.Description, "#" + l.Color}
			}
			if err := writeRows(s.Out, rows); err != nil {
				return err
			}
		}
		writeMoreLeft(s.ErrOut, more)
		return nil
	}
	return cmd
}
