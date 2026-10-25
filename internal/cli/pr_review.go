package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/evadcmd/ghzt/internal/workspace"
)

// maxCommentsFileBytes caps --comments-file. It is far more than a review's
// inline comments take, and keeps a wrong file from being read whole.
const maxCommentsFileBytes = 4 << 20

// reviewComment is an inline comment in --comments-file, as the reviews API
// takes it: line is the line number in the file, on side LEFT (the base) or
// RIGHT (the head, the default).
type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side,omitempty"`
	Body string `json:"body"`
}

func (c reviewComment) check() error {
	if err := checkRepoPath(c.Path); err != nil {
		return err
	}
	if c.Line < 1 {
		return fmt.Errorf("invalid line %d: must be at least 1", c.Line)
	}
	if c.Side != "" && c.Side != "LEFT" && c.Side != "RIGHT" {
		return fmt.Errorf("invalid side %q: valid values are {LEFT|RIGHT}", c.Side)
	}
	if c.Body == "" {
		return errors.New("body cannot be blank")
	}
	return checkBodyLen("body", c.Body)
}

// readReviewComments reads --comments-file, a JSON array of inline comments,
// inside the workspace root. A field the API doesn't take, such as a
// misspelled one, is an error rather than dropped.
func readReviewComments(s IO, name string) ([]reviewComment, error) {
	root, err := workspace.FromEnv(s.getenv)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadInput(name, s.In, maxCommentsFileBytes)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var comments []reviewComment
	if err := dec.Decode(&comments); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	// A null decodes into a nil slice without an error.
	if comments == nil {
		return nil, fmt.Errorf("%s: expected a JSON array", name)
	}
	// More misses a stray ] or }, so ask for one more token instead.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: unexpected data after the JSON array", name)
	}
	for i, c := range comments {
		if err := c.check(); err != nil {
			return nil, fmt.Errorf("%s: comment %d: %w", name, i+1, err)
		}
	}
	return comments, nil
}

func newPRReviewCmd(s IO) *cobra.Command {
	var (
		approve, comment, requestChanges bool
		commentsFile                     string
	)
	cmd := &cobra.Command{
		Use:   "review <number>",
		Short: "Add a review to a pull request",
		Long: "Add a review to a pull request.\n\n" +
			"--comments-file adds inline comments: a JSON array of\n" +
			"{\"path\", \"line\", \"side\", \"body\"} objects, where line is the line\n" +
			"number in the file and side is LEFT (the base) or RIGHT (the head, the\n" +
			"default). A body is required to request changes, and to comment\n" +
			"without inline comments. Prints the URL of the review.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "Specify the body of a review")
	cmd.Flags().BoolVarP(&approve, "approve", "a", false, "Approve pull request")
	cmd.Flags().BoolVarP(&comment, "comment", "c", false, "Comment on a pull request")
	cmd.Flags().BoolVarP(&requestChanges, "request-changes", "r", false, "Request changes on a pull request")
	cmd.Flags().StringVar(&commentsFile, "comments-file", "", "Read inline comments from a JSON file (use \"-\" to read from standard input)")
	cmd.MarkFlagsMutuallyExclusive("approve", "comment", "request-changes")
	cmd.MarkFlagsOneRequired("approve", "comment", "request-changes")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		if commentsFile == "-" && c.Flags().Changed("body-file") && body.file == "-" {
			return errors.New("--body-file and --comments-file can't both read standard input")
		}
		var comments []reviewComment
		if c.Flags().Changed("comments-file") {
			if comments, err = readReviewComments(s, commentsFile); err != nil {
				return err
			}
		}
		text := ""
		if body.given(c) {
			if text, err = body.read(s, c); err != nil {
				return err
			}
		}
		event := "APPROVE"
		switch {
		case requestChanges:
			event = "REQUEST_CHANGES"
			if text == "" {
				return errors.New("body cannot be blank for request-changes review")
			}
		case comment:
			event = "COMMENT"
			if text == "" && len(comments) == 0 {
				return errors.New("body cannot be blank for comment review without inline comments")
			}
		}
		fields := record{"event": event}
		if text != "" {
			fields["body"] = text
		}
		if len(comments) > 0 {
			fields["comments"] = comments
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		var review apiReview
		if err := api.send(ctx, http.MethodPost, repo.ref.path("pulls", strconv.Itoa(number), "reviews"), fields, &review); err != nil {
			return err
		}
		return writeURL(s.Out, review.URL)
	}
	return cmd
}

// parseCommentID parses the ID of an inline review comment, the number after
// "#discussion_r" in its URL.
func parseCommentID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("invalid comment ID %q", s)
	}
	return id, nil
}

// threadStart returns the ID of the comment that starts the review thread
// comment id is in: replies point to it, never to another reply. The comment
// is looked up by repo alone, so it is checked to be on pull request number.
func threadStart(ctx context.Context, api *apiClient, repo repoRef, number int, id int64) (int64, error) {
	var comment apiReviewComment
	if _, err := api.get(ctx, repo.path("pulls", "comments", strconv.FormatInt(id, 10)), nil, &comment); err != nil {
		return 0, err
	}
	if !strings.HasSuffix(comment.PullRequestURL, "/pulls/"+strconv.Itoa(number)) {
		return 0, fmt.Errorf("comment %d is not in a review thread of pull request %s#%d", id, repo, number)
	}
	if comment.InReplyToID != nil {
		return *comment.InReplyToID, nil
	}
	return id, nil
}

// newReviewReplyCmd is ghzt review-reply. gh has no command for it: it
// replies in the thread of an inline review comment.
func newReviewReplyCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-reply <number> <comment-id>",
		Short: "Reply to an inline review comment on a pull request",
		Long: "Reply in the thread of an inline review comment on a pull request.\n\n" +
			"The comment ID may be any comment in the thread: GitHub takes replies\n" +
			"to the comment that starts it only, so a reply's ID is looked up first.\n" +
			"Prints the URL of the reply.",
		Args: cobra.ExactArgs(2),
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "The reply body text")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		id, err := parseCommentID(args[1])
		if err != nil {
			return err
		}
		text, err := body.readRequired(s, c)
		if err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		start, err := threadStart(ctx, api, repo.ref, number, id)
		if err != nil {
			return err
		}
		var reply apiReviewComment
		path := repo.ref.path("pulls", strconv.Itoa(number), "comments", strconv.FormatInt(start, 10), "replies")
		if err := api.send(ctx, http.MethodPost, path, record{"body": text}, &reply); err != nil {
			return err
		}
		return writeURL(s.Out, reply.URL)
	}
	return cmd
}

// bigInt is a GraphQL BigInt, which GitHub sends as a decimal string.
type bigInt int64

func (b *bigInt) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	n, err := strconv.ParseInt(strings.Trim(string(data), `"`), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid BigInt %s", data)
	}
	*b = bigInt(n)
	return nil
}

// The review threads of a pull request, each with the ID of the comment that
// starts it. fullDatabaseId is that comment's REST ID; databaseId is a 32-bit
// Int, which comment IDs have outgrown.
const reviewThreadsQuery = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { id isResolved comments(first: 1) { nodes { fullDatabaseId url } } }
      }
    }
  }
}`

const resolveThreadMutation = `mutation($id: ID!) {
  resolveReviewThread(input: {threadId: $id}) { thread { isResolved } }
}`

type reviewThread struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"isResolved"`
	Comments   struct {
		Nodes []struct {
			FullDatabaseID bigInt `json:"fullDatabaseId"`
			URL            string `json:"url"`
		} `json:"nodes"`
	} `json:"comments"`
}

// findReviewThread returns the review thread of pull request number that
// comment start starts.
func findReviewThread(ctx context.Context, api *apiClient, repo repoRef, number int, start int64) (reviewThread, error) {
	vars := record{"owner": repo.owner, "name": repo.name, "number": number}
	for {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []reviewThread `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		if err := api.graphql(ctx, reviewThreadsQuery, vars, &data); err != nil {
			return reviewThread{}, err
		}
		if data.Repository == nil || data.Repository.PullRequest == nil {
			return reviewThread{}, fmt.Errorf("pull request %s#%d not found", repo, number)
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, t := range threads.Nodes {
			if len(t.Comments.Nodes) > 0 && int64(t.Comments.Nodes[0].FullDatabaseID) == start {
				return t, nil
			}
		}
		if !threads.PageInfo.HasNextPage {
			return reviewThread{}, fmt.Errorf("comment %d is not in a review thread of pull request %s#%d", start, repo, number)
		}
		vars["after"] = threads.PageInfo.EndCursor
	}
}

// newReviewResolveCmd is ghzt review-resolve. gh has no command for it, and
// the REST API no endpoint: it resolves a review thread through GraphQL.
func newReviewResolveCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-resolve <number> <comment-id>",
		Short: "Resolve a review thread on a pull request",
		Long: "Resolve the review thread an inline review comment is in, as the\n" +
			"\"Resolve conversation\" button does. The comment ID may be any comment\n" +
			"in the thread. A thread that is resolved already is left as it is.\n" +
			"Prints the URL of the comment that starts the thread.",
		Args: cobra.ExactArgs(2),
	}
	repo := addRepoFlag(cmd)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		id, err := parseCommentID(args[1])
		if err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		start, err := threadStart(ctx, api, repo.ref, number, id)
		if err != nil {
			return err
		}
		thread, err := findReviewThread(ctx, api, repo.ref, number, start)
		if err != nil {
			return err
		}
		if !thread.IsResolved {
			var data struct {
				ResolveReviewThread struct {
					Thread struct {
						IsResolved bool `json:"isResolved"`
					} `json:"thread"`
				} `json:"resolveReviewThread"`
			}
			if err := api.graphql(ctx, resolveThreadMutation, record{"id": thread.ID}, &data); err != nil {
				return err
			}
			if !data.ResolveReviewThread.Thread.IsResolved {
				return errors.New("GitHub did not resolve the thread")
			}
		}
		return writeURL(s.Out, thread.Comments.Nodes[0].URL)
	}
	return cmd
}
