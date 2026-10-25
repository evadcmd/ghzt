package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newIssueCreateCmd(s IO) *cobra.Command {
	var (
		title             string
		labels, assignees []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new issue",
		Long:  "Create a new issue.\n\nPrints the URL of the new issue.",
		Args:  cobra.NoArgs,
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "Supply a body")
	cmd.Flags().StringVarP(&title, "title", "t", "", "Supply a title")
	cmd.Flags().StringSliceVarP(&labels, "label", "l", nil, "Add labels by name")
	cmd.Flags().StringSliceVarP(&assignees, "assignee", "a", nil, "Assign people by their login")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if title == "" {
			return errors.New("--title required when not running interactively")
		}
		fields := record{"title": title}
		if body.given(c) {
			text, err := body.read(s, c)
			if err != nil {
				return err
			}
			fields["body"] = text
		}
		if len(assignees) > 0 {
			fields["assignees"] = assignees
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		if len(labels) > 0 {
			names, err := resolveLabels(ctx, api, repo.ref, labels)
			if err != nil {
				return err
			}
			fields["labels"] = names
		}
		var issue apiIssue
		if err := api.send(ctx, http.MethodPost, repo.ref.path("issues"), fields, &issue); err != nil {
			return err
		}
		return writeURL(s.Out, issue.URL)
	}
	return cmd
}

// closeReasons maps gh's --reason values to the API's state_reason. gh
// spells one "not planned"; the API's "not_planned" is taken too.
var closeReasons = map[string]string{
	"completed":   "completed",
	"not planned": "not_planned",
	"not_planned": "not_planned",
	"duplicate":   "duplicate",
}

func newIssueCloseCmd(s IO) *cobra.Command {
	var reason, comment string
	cmd := &cobra.Command{
		Use:   "close <number>",
		Short: "Close an issue",
		Long:  "Close an issue.\n\nPrints the URL of the issue.",
		Args:  cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().StringVarP(&reason, "reason", "r", "", "Reason for closing: {completed|not planned|duplicate}")
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Leave a closing comment")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		fields := record{"state": "closed"}
		if reason != "" {
			stateReason, ok := closeReasons[reason]
			if !ok {
				return checkEnum("reason", reason, "completed", "not planned", "duplicate")
			}
			fields["state_reason"] = stateReason
		}
		if err := checkBodyLen("comment", comment); err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		n := strconv.Itoa(number)
		// The comment goes first, as in gh, so that it reads as the reason
		// above the close event.
		if comment != "" {
			if err := api.send(ctx, http.MethodPost, repo.ref.path("issues", n, "comments"), record{"body": comment}, nil); err != nil {
				return err
			}
		}
		var issue apiIssue
		if err := api.send(ctx, http.MethodPatch, repo.ref.path("issues", n), fields, &issue); err != nil {
			return err
		}
		return writeURL(s.Out, issue.URL)
	}
	return cmd
}

func newIssueEditCmd(s IO) *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "edit <number>",
		Short: "Edit an issue",
		Long: "Edit an issue.\n\n" +
			"Labels and assignees are added and removed by reading the current\n" +
			"ones and sending back the whole set, as the API takes it. Prints the\n" +
			"URL of the issue.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "Set the new body")
	cmd.Flags().StringVarP(&title, "title", "t", "", "Set the new title")
	sets := addIssueSetEditFlags(cmd)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		fields := record{}
		if c.Flags().Changed("title") {
			if title == "" {
				return errors.New("title cannot be blank")
			}
			fields["title"] = title
		}
		if body.given(c) {
			text, err := body.read(s, c)
			if err != nil {
				return err
			}
			fields["body"] = text
		}
		if len(fields) == 0 && !sets.changed() {
			return errNothingToEdit
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		if err := sets.addTo(ctx, api, repo.ref, number, fields); err != nil {
			return err
		}
		var issue apiIssue
		if err := api.send(ctx, http.MethodPatch, repo.ref.path("issues", strconv.Itoa(number)), fields, &issue); err != nil {
			return err
		}
		return writeURL(s.Out, issue.URL)
	}
	return cmd
}

var errNothingToEdit = errors.New("field to edit flag required when not running interactively")

// issueSetEdits are the label and assignee flags gh issue edit and gh pr edit
// share. Both go through the issues API: a pull request is an issue there.
type issueSetEdits struct {
	labels, assignees *setEdits
}

func addIssueSetEditFlags(cmd *cobra.Command) issueSetEdits {
	return issueSetEdits{
		labels:    addSetEditFlags(cmd, "label", "labels"),
		assignees: addSetEditFlags(cmd, "assignee", "assigned users"),
	}
}

func (e issueSetEdits) changed() bool { return e.labels.changed() || e.assignees.changed() }

// addTo reads the issue's labels and assignees and puts the edited sets into
// fields, the request body of an issue update. Only a set that is edited is
// sent, so the other one is left as it is.
func (e issueSetEdits) addTo(ctx context.Context, api *apiClient, repo repoRef, number int, fields record) error {
	if !e.changed() {
		return nil
	}
	var issue apiIssue
	if _, err := api.get(ctx, repo.path("issues", strconv.Itoa(number)), nil, &issue); err != nil {
		return err
	}
	if e.labels.changed() {
		add, err := resolveLabels(ctx, api, repo, e.labels.add)
		if err != nil {
			return err
		}
		names := make([]string, len(issue.Labels))
		for i, l := range issue.Labels {
			names[i] = l.Name
		}
		edits := setEdits{add: add, remove: e.labels.remove}
		fields["labels"] = edits.apply(names)
	}
	if e.assignees.changed() {
		logins := make([]string, len(issue.Assignees))
		for i, u := range issue.Assignees {
			logins[i] = u.Login
		}
		fields["assignees"] = e.assignees.apply(logins)
	}
	return nil
}

// resolveLabels returns names spelled as the repo's labels are, or an error
// naming the first one the repo doesn't have. GitHub creates a label it
// doesn't know when an issue is given one, so without this a typo would add
// a label to the repo; gh looks the names up first too.
func resolveLabels(ctx context.Context, api *apiClient, repo repoRef, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	labels, _, err := list[apiLabel](ctx, api, repo.path("labels"), nil, 0, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(names))
	for i, name := range names {
		j := slices.IndexFunc(labels, func(l apiLabel) bool { return strings.EqualFold(l.Name, name) })
		if j < 0 {
			return nil, fmt.Errorf("could not add label: '%s' not found", name)
		}
		out[i] = labels[j].Name
	}
	return out, nil
}
