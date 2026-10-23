package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

func newLabelCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label <command>",
		Short: "Work with GitHub labels",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newLabelListCmd(s))
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
					"url":         "https://github.com/" + repo.ref.String() + "/labels/" + url.PathEscape(l.Name),
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
