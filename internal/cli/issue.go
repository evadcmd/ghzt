package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// The REST API objects the commands read, with only the fields they use.
type (
	apiUser struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"`
		URL   string `json:"html_url"`
	}
	apiLabel struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
		Default     bool   `json:"default"`
	}
	apiIssue struct {
		Number        int        `json:"number"`
		Title         string     `json:"title"`
		State         string     `json:"state"`
		StateReason   string     `json:"state_reason"`
		Body          string     `json:"body"`
		User          *apiUser   `json:"user"`
		Labels        []apiLabel `json:"labels"`
		Comments      int        `json:"comments"`
		URL           string     `json:"html_url"`
		RepositoryURL string     `json:"repository_url"`
		PullRequest   *struct{}  `json:"pull_request"`
		CreatedAt     string     `json:"created_at"`
		UpdatedAt     string     `json:"updated_at"`
		ClosedAt      string     `json:"closed_at"`
	}
	apiComment struct {
		ID        int64    `json:"id"`
		User      *apiUser `json:"user"`
		Body      string   `json:"body"`
		URL       string   `json:"html_url"`
		CreatedAt string   `json:"created_at"`
	}
)

func newIssueCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue <command>",
		Short: "Work with GitHub issues",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newIssueViewCmd(s))
	return cmd
}

var issueFields = []string{
	"author", "body", "closedAt", "comments", "createdAt", "labels", "number",
	"state", "stateReason", "title", "updatedAt", "url",
}

func newIssueViewCmd(s IO) *cobra.Command {
	var comments bool
	cmd := &cobra.Command{
		Use:   "view <number>",
		Short: "View an issue",
		Args:  cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	js := addJSONFlag(cmd, issueFields...)
	cmd.Flags().BoolVarP(&comments, "comments", "c", false, "View issue comments")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		var issue apiIssue
		if _, err := api.get(ctx, repo.ref.path("issues", strconv.Itoa(number)), nil, &issue); err != nil {
			return err
		}
		var cs []apiComment
		if comments || js.wants("comments") {
			if cs, err = issueComments(ctx, api, repo.ref, number); err != nil {
				return err
			}
		}
		if js.enabled() {
			r := issueRecord(issue)
			r["comments"] = commentRecords(cs)
			return js.writeRecord(s.Out, r)
		}
		return writeIssue(s.Out, issue, cs, comments)
	}
	return cmd
}

// parseNumber parses an issue or PR number, as gh takes it: "123" or "#123".
func parseNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid issue or pull request number: %q", s)
	}
	return n, nil
}

func issueComments(ctx context.Context, api *apiClient, repo repoRef, number int) ([]apiComment, error) {
	cs, _, err := list[apiComment](ctx, api, repo.path("issues", strconv.Itoa(number), "comments"), nil, 0, nil)
	return cs, err
}

func issueRecord(i apiIssue) record {
	return record{
		"number":      i.Number,
		"title":       i.Title,
		"state":       strings.ToUpper(i.State),
		"stateReason": strings.ToUpper(i.StateReason),
		"author":      login(i.User),
		"labels":      labelRecords(i.Labels),
		"body":        i.Body,
		"url":         i.URL,
		"createdAt":   i.CreatedAt,
		"updatedAt":   i.UpdatedAt,
		"closedAt":    nullable(i.ClosedAt),
	}
}

func commentRecords(cs []apiComment) []record {
	out := make([]record, len(cs))
	for i, c := range cs {
		out[i] = record{
			"id":        c.ID,
			"author":    login(c.User),
			"body":      c.Body,
			"createdAt": c.CreatedAt,
			"url":       c.URL,
		}
	}
	return out
}

// writeIssue prints the issue the way gh issue view does when stdout isn't a
// terminal: fields, "--", the body, then with --comments each comment the
// same way.
func writeIssue(w io.Writer, i apiIssue, cs []apiComment, comments bool) error {
	if comments {
		return writeComments(w, cs)
	}
	state := strings.ToUpper(i.State)
	if i.StateReason != "" && i.State == "closed" {
		state += " (" + strings.ToUpper(i.StateReason) + ")"
	}
	if err := writeFields(w,
		"title", i.Title,
		"state", state,
		"author", loginText(i.User),
		"labels", labelNames(i.Labels),
		"comments", strconv.Itoa(i.Comments),
		"number", strconv.Itoa(i.Number),
		"url", i.URL,
	); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "--\n%s\n", truncateText(i.Body, maxBodyLen))
	return err
}

// writeComments prints comments as gh does with --comments when stdout isn't
// a terminal, plus each one's id, which replies and edits need.
func writeComments(w io.Writer, cs []apiComment) error {
	for _, c := range cs {
		if err := writeFields(w,
			"author", loginText(c.User),
			"id", strconv.FormatInt(c.ID, 10),
			"created", c.CreatedAt,
		); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "--\n%s\n--\n", truncateText(c.Body, maxBodyLen)); err != nil {
			return err
		}
	}
	return nil
}
