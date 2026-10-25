package cli

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// parseHead parses a head branch, "branch" or "owner:branch"; gh takes a
// bare branch as one in the repo itself.
func parseHead(repo repoRef, head string) (owner, branch string, err error) {
	owner, branch, ok := strings.Cut(head, ":")
	if !ok {
		owner, branch = repo.owner, head
	}
	if err := checkOwner(owner); err != nil {
		return "", "", err
	}
	if err := checkBranch(branch); err != nil {
		return "", "", err
	}
	return owner, branch, nil
}

func newPRCreateCmd(s IO) *cobra.Command {
	var (
		title, base, head string
		draft             bool
		labels, assignees []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a pull request",
		Long: "Create a pull request.\n\n" +
			"--head is required: there is no local branch to take it from. --base\n" +
			"defaults to the repository's default branch. Prints the URL of the new\n" +
			"pull request.",
		Args: cobra.NoArgs,
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "Body for the pull request")
	cmd.Flags().StringVarP(&title, "title", "t", "", "Title for the pull request")
	cmd.Flags().StringVarP(&base, "base", "B", "", "The branch into which you want your code merged")
	cmd.Flags().StringVarP(&head, "head", "H", "", "The branch that contains commits for your pull request (\"branch\" or \"owner:branch\")")
	cmd.Flags().BoolVarP(&draft, "draft", "d", false, "Mark pull request as a draft")
	cmd.Flags().StringSliceVarP(&labels, "label", "l", nil, "Add labels by name")
	cmd.Flags().StringSliceVarP(&assignees, "assignee", "a", nil, "Assign people by their login")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if title == "" {
			return errors.New("--title required when not running interactively")
		}
		if head == "" {
			return errors.New("--head required: there is no local branch to take it from")
		}
		owner, branch, err := parseHead(repo.ref, head)
		if err != nil {
			return err
		}
		if owner != repo.ref.owner {
			branch = owner + ":" + branch
		}
		if base != "" {
			if err := checkBranch(base); err != nil {
				return err
			}
		}
		fields := record{"title": title, "head": branch, "draft": draft}
		if body.given(c) {
			text, err := body.read(s, c)
			if err != nil {
				return err
			}
			fields["body"] = text
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		if base == "" {
			var r apiRepo
			if _, err := api.get(ctx, repo.ref.path(), nil, &r); err != nil {
				return err
			}
			base = r.DefaultBranch
		}
		fields["base"] = base
		// The labels are looked up before the pull request is made, so a
		// name the repo doesn't have fails the command without leaving one.
		labelNames, err := resolveLabels(ctx, api, repo.ref, labels)
		if err != nil {
			return err
		}
		var pr apiPull
		if err := api.send(ctx, http.MethodPost, repo.ref.path("pulls"), fields, &pr); err != nil {
			return err
		}
		// The URL goes out before the labels and assignees are set, so that
		// if setting them fails, the caller still knows the pull request
		// exists.
		if err := writeURL(s.Out, pr.URL); err != nil {
			return err
		}
		if len(labelNames) == 0 && len(assignees) == 0 {
			return nil
		}
		issueFields := record{}
		if len(labelNames) > 0 {
			issueFields["labels"] = labelNames
		}
		if len(assignees) > 0 {
			issueFields["assignees"] = assignees
		}
		return api.send(ctx, http.MethodPatch, repo.ref.path("issues", strconv.Itoa(pr.Number)), issueFields, nil)
	}
	return cmd
}

func newPREditCmd(s IO) *cobra.Command {
	var title, base string
	cmd := &cobra.Command{
		Use:   "edit <number>",
		Short: "Edit a pull request",
		Long: "Edit a pull request.\n\n" +
			"Labels and assignees are added and removed by reading the current\n" +
			"ones and sending back the whole set, as the API takes it. Prints the\n" +
			"URL of the pull request.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "Set the new body")
	cmd.Flags().StringVarP(&title, "title", "t", "", "Set the new title")
	cmd.Flags().StringVarP(&base, "base", "B", "", "Change the base branch for this pull request")
	sets := addIssueSetEditFlags(cmd)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		// Title, body and base are the pull request's; labels and
		// assignees are set through the issues API.
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
		if c.Flags().Changed("base") {
			if err := checkBranch(base); err != nil {
				return err
			}
			fields["base"] = base
		}
		if len(fields) == 0 && !sets.changed() {
			return errNothingToEdit
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		n := strconv.Itoa(number)
		var u string
		if len(fields) > 0 {
			var pr apiPull
			if err := api.send(ctx, http.MethodPatch, repo.ref.path("pulls", n), fields, &pr); err != nil {
				return err
			}
			u = pr.URL
		}
		if sets.changed() {
			issueFields := record{}
			if err := sets.addTo(ctx, api, repo.ref, number, issueFields); err != nil {
				return err
			}
			var issue apiIssue
			if err := api.send(ctx, http.MethodPatch, repo.ref.path("issues", n), issueFields, &issue); err != nil {
				return err
			}
			u = issue.URL
		}
		return writeURL(s.Out, u)
	}
	return cmd
}
