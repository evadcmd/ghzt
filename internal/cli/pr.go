package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type (
	apiRef struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	apiPull struct {
		Number       int        `json:"number"`
		Title        string     `json:"title"`
		State        string     `json:"state"`
		Draft        bool       `json:"draft"`
		Body         string     `json:"body"`
		User         *apiUser   `json:"user"`
		Labels       []apiLabel `json:"labels"`
		Head         apiRef     `json:"head"`
		Base         apiRef     `json:"base"`
		Mergeable    *bool      `json:"mergeable"`
		Additions    int        `json:"additions"`
		Deletions    int        `json:"deletions"`
		ChangedFiles int        `json:"changed_files"`
		URL          string     `json:"html_url"`
		CreatedAt    string     `json:"created_at"`
		UpdatedAt    string     `json:"updated_at"`
		ClosedAt     string     `json:"closed_at"`
		MergedAt     string     `json:"merged_at"`
	}
	apiCommitAuthor struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Date  string `json:"date"`
	}
	apiPullCommit struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message   string          `json:"message"`
			Author    apiCommitAuthor `json:"author"`
			Committer apiCommitAuthor `json:"committer"`
		} `json:"commit"`
		Author *apiUser `json:"author"`
	}
	apiReview struct {
		ID          int64    `json:"id"`
		User        *apiUser `json:"user"`
		State       string   `json:"state"`
		Body        string   `json:"body"`
		CommitID    string   `json:"commit_id"`
		SubmittedAt string   `json:"submitted_at"`
		URL         string   `json:"html_url"`
	}
	apiReviewComment struct {
		ID                  int64    `json:"id"`
		InReplyToID         *int64   `json:"in_reply_to_id"`
		PullRequestReviewID *int64   `json:"pull_request_review_id"`
		Path                string   `json:"path"`
		Line                *int     `json:"line"`
		Side                string   `json:"side"`
		Body                string   `json:"body"`
		User                *apiUser `json:"user"`
		URL                 string   `json:"html_url"`
		CreatedAt           string   `json:"created_at"`
	}
)

func newPRCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr <command>",
		Short: "Work with GitHub pull requests",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(newPRViewCmd(s), newPRListCmd(s), newPRCreateCmd(s), newPREditCmd(s),
		newCommentCmd(s, "a pull request"), newPRReviewCmd(s))
	return cmd
}

// prListFields are the fields the list endpoint has; view adds the ones that
// GitHub only computes for a single pull request, and the ones that take
// requests of their own.
var (
	prListFields = []string{
		"author", "baseRefName", "body", "closedAt", "createdAt", "headRefName",
		"isDraft", "labels", "mergedAt", "number", "state", "title", "updatedAt", "url",
	}
	prViewFields = append(slices.Clone(prListFields),
		"additions", "changedFiles", "comments", "commits", "deletions", "files", "mergeable",
		"reviewComments", "reviews",
	)
)

func newPRViewCmd(s IO) *cobra.Command {
	var comments bool
	cmd := &cobra.Command{
		Use:   "view <number>",
		Short: "View a pull request",
		Long: "View a pull request.\n\n" +
			"With --comments, the conversation comments, reviews and inline review\n" +
			"comments are shown in time order. --json reviewComments lists the inline\n" +
			"comments with inReplyToId and pullRequestReviewId, to group them into\n" +
			"threads and reviews.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	js := addJSONFlag(cmd, prViewFields...)
	cmd.Flags().BoolVarP(&comments, "comments", "c", false, "View pull request comments and reviews")
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
		n := strconv.Itoa(number)
		var pr apiPull
		if _, err := api.get(ctx, repo.ref.path("pulls", n), nil, &pr); err != nil {
			return err
		}
		var (
			cs      []apiComment
			reviews []apiReview
			inline  []apiReviewComment
		)
		if comments || js.wants("comments") {
			if cs, err = issueComments(ctx, api, repo.ref, number); err != nil {
				return err
			}
		}
		if comments || js.wants("reviews") {
			if reviews, _, err = list[apiReview](ctx, api, repo.ref.path("pulls", n, "reviews"), nil, 0, nil); err != nil {
				return err
			}
		}
		if comments || js.wants("reviewComments") {
			if inline, _, err = list[apiReviewComment](ctx, api, repo.ref.path("pulls", n, "comments"), nil, 0, nil); err != nil {
				return err
			}
		}
		if !js.enabled() {
			if comments {
				n, err := writeConversation(s.Out, cs, reviews, inline)
				if err != nil {
					return err
				}
				writeNoResults(s.ErrOut, n, "comments or reviews")
				return nil
			}
			return writePR(s.Out, pr)
		}
		r := prRecord(pr)
		r["additions"] = pr.Additions
		r["deletions"] = pr.Deletions
		r["changedFiles"] = pr.ChangedFiles
		r["mergeable"] = mergeableState(pr.Mergeable)
		r["comments"] = commentRecords(cs)
		r["reviews"] = reviewRecords(reviews)
		r["reviewComments"] = reviewCommentRecords(inline)
		if js.wants("commits") {
			commits, err := pullCommits(ctx, api, repo.ref, number)
			if err != nil {
				return err
			}
			r["commits"] = commits
		}
		if js.wants("files") {
			files, err := pullFiles(ctx, api, repo.ref, number)
			if err != nil {
				return err
			}
			r["files"] = files
		}
		return js.writeRecord(s.Out, r)
	}
	return cmd
}

func newPRListCmd(s IO) *cobra.Command {
	var (
		state, base, head string
		limit             int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pull requests in a repository",
		Args:  cobra.NoArgs,
	}
	repo := addRepoFlag(cmd)
	js := addJSONFlag(cmd, prListFields...)
	cmd.Flags().StringVarP(&state, "state", "s", "open", "Filter by state: {open|closed|merged|all}")
	cmd.Flags().StringVarP(&base, "base", "B", "", "Filter by base branch")
	cmd.Flags().StringVarP(&head, "head", "H", "", "Filter by head branch (\"branch\" or \"owner:branch\")")
	cmd.Flags().IntVarP(&limit, "limit", "L", 30, "Maximum number of items to fetch")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if err := checkEnum("state", state, "open", "closed", "merged", "all"); err != nil {
			return err
		}
		if err := checkLimit(limit); err != nil {
			return err
		}
		q := url.Values{"state": {state}, "sort": {"created"}, "direction": {"desc"}}
		var keep func(apiPull) bool
		if state == "merged" {
			// The API has no merged state: merged pull requests are closed
			// ones with a merge time.
			q.Set("state", "closed")
			keep = func(p apiPull) bool { return p.MergedAt != "" }
		}
		if base != "" {
			if err := checkBranch(base); err != nil {
				return err
			}
			q.Set("base", base)
		}
		if head != "" {
			// The API wants "owner:branch" here.
			owner, branch, err := parseHead(repo.ref, head)
			if err != nil {
				return err
			}
			q.Set("head", owner+":"+branch)
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		prs, more, err := list(ctx, api, repo.ref.path("pulls"), q, limit, keep)
		if err != nil {
			return err
		}
		if js.enabled() {
			rs := make([]record, len(prs))
			for i, pr := range prs {
				rs[i] = prRecord(pr)
			}
			if err := js.writeRecords(s.Out, rs); err != nil {
				return err
			}
		} else {
			rows := make([][]string, len(prs))
			for i, pr := range prs {
				state := prState(pr)
				if pr.Draft && state == "OPEN" {
					state = "DRAFT"
				}
				rows[i] = []string{strconv.Itoa(pr.Number), pr.Title, pr.Head.Ref, state, pr.CreatedAt}
			}
			if err := writeRows(s.Out, rows); err != nil {
				return err
			}
			writeNoResults(s.ErrOut, len(rows), "pull requests")
		}
		writeMoreLeft(s.ErrOut, more)
		return nil
	}
	return cmd
}

// prState is gh's state: OPEN, CLOSED or MERGED.
func prState(pr apiPull) string {
	if pr.MergedAt != "" {
		return "MERGED"
	}
	return strings.ToUpper(pr.State)
}

// mergeableState is gh's mergeable: UNKNOWN while GitHub is still working it
// out, which the REST API says with null.
func mergeableState(m *bool) string {
	switch {
	case m == nil:
		return "UNKNOWN"
	case *m:
		return "MERGEABLE"
	default:
		return "CONFLICTING"
	}
}

func prRecord(pr apiPull) record {
	return record{
		"number":      pr.Number,
		"title":       pr.Title,
		"state":       prState(pr),
		"isDraft":     pr.Draft,
		"author":      login(pr.User),
		"labels":      labelRecords(pr.Labels),
		"body":        pr.Body,
		"headRefName": pr.Head.Ref,
		"baseRefName": pr.Base.Ref,
		"url":         pr.URL,
		"createdAt":   pr.CreatedAt,
		"updatedAt":   pr.UpdatedAt,
		"closedAt":    nullable(pr.ClosedAt),
		"mergedAt":    nullable(pr.MergedAt),
	}
}

// pullCommits returns gh's commits field. The API lists at most 250 commits
// of a pull request.
func pullCommits(ctx context.Context, api *apiClient, repo repoRef, number int) ([]record, error) {
	commits, _, err := list[apiPullCommit](ctx, api, repo.path("pulls", strconv.Itoa(number), "commits"), nil, 0, nil)
	if err != nil {
		return nil, err
	}
	out := make([]record, len(commits))
	for i, c := range commits {
		headline, body, _ := strings.Cut(c.Commit.Message, "\n")
		author := record{"name": c.Commit.Author.Name, "email": c.Commit.Author.Email}
		if c.Author != nil {
			author["login"] = c.Author.Login
		}
		out[i] = record{
			"oid":             c.SHA,
			"messageHeadline": headline,
			"messageBody":     strings.TrimLeft(body, "\n"),
			"authors":         []record{author},
			"authoredDate":    c.Commit.Author.Date,
			"committedDate":   c.Commit.Committer.Date,
		}
	}
	return out, nil
}

// pullFiles returns the pull request's changed files as gh's files field. The
// API lists at most 3000.
func pullFiles(ctx context.Context, api *apiClient, repo repoRef, number int) ([]record, error) {
	files, _, err := list[apiCommitFile](ctx, api, repo.path("pulls", strconv.Itoa(number), "files"), nil, 0, nil)
	if err != nil {
		return nil, err
	}
	out := make([]record, len(files))
	for i, f := range files {
		out[i] = record{
			"path":       f.Filename,
			"additions":  f.Additions,
			"deletions":  f.Deletions,
			"changeType": changeType(f.Status),
		}
	}
	return out, nil
}

// changeType is gh's changeType for the API's file status: the same word in
// capitals, except that the API's "removed" is gh's "DELETED".
func changeType(status string) string {
	if status == "removed" {
		return "DELETED"
	}
	return strings.ToUpper(status)
}

func reviewRecords(rs []apiReview) []record {
	out := make([]record, len(rs))
	for i, r := range rs {
		out[i] = record{
			"id":          r.ID,
			"author":      login(r.User),
			"state":       r.State,
			"body":        r.Body,
			"submittedAt": nullable(r.SubmittedAt),
			"commit":      record{"oid": r.CommitID},
		}
	}
	return out
}

func reviewCommentRecords(cs []apiReviewComment) []record {
	out := make([]record, len(cs))
	for i, c := range cs {
		out[i] = record{
			"id":                  c.ID,
			"inReplyToId":         c.InReplyToID,
			"pullRequestReviewId": c.PullRequestReviewID,
			"path":                c.Path,
			"line":                c.Line,
			"side":                c.Side,
			"body":                c.Body,
			"author":              login(c.User),
			"url":                 c.URL,
			"createdAt":           c.CreatedAt,
		}
	}
	return out
}

// writePR prints the pull request the way gh pr view does when stdout isn't
// a terminal, with the branches added: the agent needs them to check out or
// diff the pull request.
func writePR(w io.Writer, pr apiPull) error {
	if err := writeFields(w,
		"title", pr.Title,
		"state", prState(pr),
		"author", loginText(pr.User),
		"labels", labelNames(pr.Labels),
		"number", strconv.Itoa(pr.Number),
		"url", pr.URL,
		"draft", strconv.FormatBool(pr.Draft),
		"head", pr.Head.Ref,
		"base", pr.Base.Ref,
		"mergeable", mergeableState(pr.Mergeable),
		"additions", strconv.Itoa(pr.Additions),
		"deletions", strconv.Itoa(pr.Deletions),
	); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "--\n%s\n", truncateText(pr.Body, maxBodyLen))
	return err
}

// conversationItem is one comment, review or inline comment of a pull
// request, for printing them in time order.
type conversationItem struct {
	at     string
	fields []string
	body   string
}

// writeConversation prints the conversation comments, reviews and inline
// comments of a pull request by time, each with the ids that replies need,
// and returns how many items it printed.
func writeConversation(w io.Writer, cs []apiComment, reviews []apiReview, inline []apiReviewComment) (int, error) {
	var items []conversationItem
	for _, c := range cs {
		items = append(items, conversationItem{c.CreatedAt, []string{
			"kind", "comment",
			"author", loginText(c.User),
			"id", strconv.FormatInt(c.ID, 10),
			"created", c.CreatedAt,
		}, c.Body})
	}
	for _, r := range reviews {
		// A pending review is the token owner's unsubmitted draft, and a
		// commented review without a body only wraps inline comments, which
		// carry its id.
		if r.State == "PENDING" || (r.State == "COMMENTED" && r.Body == "") {
			continue
		}
		items = append(items, conversationItem{r.SubmittedAt, []string{
			"kind", "review",
			"author", loginText(r.User),
			"id", strconv.FormatInt(r.ID, 10),
			"state", r.State,
			"created", r.SubmittedAt,
		}, r.Body})
	}
	for _, c := range inline {
		fields := []string{
			"kind", "review-comment",
			"author", loginText(c.User),
			"id", strconv.FormatInt(c.ID, 10),
			"path", c.Path,
		}
		if c.Line != nil {
			fields = append(fields, "line", strconv.Itoa(*c.Line))
		}
		if c.InReplyToID != nil {
			fields = append(fields, "reply-to", strconv.FormatInt(*c.InReplyToID, 10))
		}
		if c.PullRequestReviewID != nil {
			fields = append(fields, "review", strconv.FormatInt(*c.PullRequestReviewID, 10))
		}
		fields = append(fields, "created", c.CreatedAt)
		items = append(items, conversationItem{c.CreatedAt, fields, c.Body})
	}
	// RFC 3339 times in UTC, as the API sends them, sort as strings.
	slices.SortStableFunc(items, func(a, b conversationItem) int { return strings.Compare(a.at, b.at) })
	for _, it := range items {
		if err := writeFields(w, it.fields...); err != nil {
			return 0, err
		}
		if _, err := fmt.Fprintf(w, "--\n%s\n--\n", truncateText(it.body, maxBodyLen)); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}
