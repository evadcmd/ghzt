package cli

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

func newSearchCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <command>",
		Short: "Search for repositories, issues, pull requests, commits, code and users",
		Long: "Search across all of GitHub.\n\n" +
			"The search API returns at most the first 1000 results of a query.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(
		newSearchKindCmd(s, issueSearch("issues", "is:issue")),
		newSearchKindCmd(s, issueSearch("prs", "is:pr")),
		newSearchKindCmd(s, repoSearch),
		newSearchKindCmd(s, commitSearch),
		newSearchKindCmd(s, codeSearch),
		newSearchKindCmd(s, userSearch),
	)
	return cmd
}

// searchSpec is what one gh search subcommand searches and how it prints the
// results.
type searchSpec[T any] struct {
	use, short string
	// endpoint is the /search/<endpoint> the query goes to.
	endpoint string
	// qualifier is added to every query, to keep issues and pull requests
	// apart on the endpoint they share.
	qualifier string
	// state adds --state, for issues and pull requests.
	state  bool
	sorts  []string
	fields []string
	record func(T) record
	row    func(T) []string
	// check, if not nil, rejects queries the endpoint would.
	check func(query string) error
}

func newSearchKindCmd[T any](s IO, spec searchSpec[T]) *cobra.Command {
	var (
		owner, state, sort, order string
		repos                     repoSliceFlag
		limit                     int
	)
	cmd := &cobra.Command{
		Use:   spec.use + " [<query>]",
		Short: spec.short,
		Long: spec.short + ".\n\n" +
			"The query takes GitHub's search syntax, qualifiers included.",
		Args: cobra.ArbitraryArgs,
	}
	js := addJSONFlag(cmd, spec.fields...)
	cmd.Flags().IntVarP(&limit, "limit", "L", 30, "Maximum number of results to fetch (at most 1000)")
	cmd.Flags().VarP(&repos, "repo", "R", "Filter on repository (OWNER/REPO, repeatable)")
	cmd.Flags().StringVar(&owner, "owner", "", "Filter on owner")
	if spec.state {
		cmd.Flags().StringVar(&state, "state", "", "Filter based on state: {open|closed}")
	}
	if len(spec.sorts) > 0 {
		cmd.Flags().StringVar(&sort, "sort", "", "Sort fetched results: {"+strings.Join(spec.sorts, "|")+"}")
		cmd.Flags().StringVar(&order, "order", "desc", "Order of results returned, ignored unless --sort is given: {asc|desc}")
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if err := checkLimit(limit); err != nil {
			return err
		}
		terms := make([]string, 0, len(args)+len(repos.refs)+3)
		for _, a := range args {
			// gh quotes an argument with spaces, so that `gh search issues
			// "out of memory"` searches for the phrase.
			if strings.ContainsAny(a, " \t\n") && !strings.Contains(a, `"`) {
				a = `"` + a + `"`
			}
			terms = append(terms, a)
		}
		for _, r := range repos.refs {
			terms = append(terms, "repo:"+r.String())
		}
		if owner != "" {
			if err := checkOwner(owner); err != nil {
				return err
			}
			terms = append(terms, "user:"+owner)
		}
		if state != "" {
			if err := checkEnum("state", state, "open", "closed"); err != nil {
				return err
			}
			terms = append(terms, "state:"+state)
		}
		if spec.qualifier != "" {
			terms = append(terms, spec.qualifier)
		}
		query := strings.Join(terms, " ")
		if strings.TrimSpace(strings.TrimSuffix(query, spec.qualifier)) == "" {
			return errors.New("specify search keywords or flags")
		}
		if spec.check != nil {
			if err := spec.check(query); err != nil {
				return err
			}
		}
		q := url.Values{"q": {query}}
		if sort != "" {
			if err := checkEnum("sort", sort, spec.sorts...); err != nil {
				return err
			}
			if err := checkEnum("order", order, "asc", "desc"); err != nil {
				return err
			}
			q.Set("sort", sort)
			q.Set("order", order)
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		items, total, err := search[T](ctx, api, spec.endpoint, q, limit)
		if err != nil {
			return err
		}
		if js.enabled() {
			rs := make([]record, len(items))
			for i, it := range items {
				rs[i] = spec.record(it)
			}
			if err := js.writeRecords(s.Out, rs); err != nil {
				return err
			}
		} else {
			rows := make([][]string, len(items))
			for i, it := range items {
				rows[i] = spec.row(it)
			}
			if err := writeRows(s.Out, rows); err != nil {
				return err
			}
		}
		writeListTruncated(s.ErrOut, len(items), min(total, maxSearchResults))
		return nil
	}
	return cmd
}

// repoName is gh's repository object for a search result.
func repoName(fullName string) record {
	return record{"nameWithOwner": fullName}
}

func issueSearch(use, qualifier string) searchSpec[apiIssue] {
	what := "issues"
	if use == "prs" {
		what = "pull requests"
	}
	return searchSpec[apiIssue]{
		use:       use,
		short:     "Search for " + what,
		endpoint:  "issues",
		qualifier: qualifier,
		state:     true,
		sorts:     []string{"comments", "created", "interactions", "reactions", "updated"},
		fields: []string{
			"author", "body", "commentsCount", "createdAt", "isPullRequest", "labels",
			"number", "repository", "state", "title", "updatedAt", "url",
		},
		record: func(i apiIssue) record {
			return record{
				"number":        i.Number,
				"title":         i.Title,
				"state":         i.State,
				"author":        login(i.User),
				"isPullRequest": i.PullRequest != nil,
				"repository":    repoName(issueRepo(i)),
				"url":           i.URL,
				"createdAt":     i.CreatedAt,
				"updatedAt":     i.UpdatedAt,
				"labels":        labelRecords(i.Labels),
				"commentsCount": i.Comments,
				"body":          i.Body,
			}
		},
		row: func(i apiIssue) []string {
			return []string{issueRepo(i), strconv.Itoa(i.Number), i.State, i.Title, labelNames(i.Labels), i.UpdatedAt}
		},
	}
}

// issueRepo is the owner/repo of a search result, which only has the API URL
// of its repository.
func issueRepo(i apiIssue) string {
	_, name, _ := strings.Cut(i.RepositoryURL, "/repos/")
	return name
}

type apiRepo struct {
	FullName        string `json:"full_name"`
	Description     string `json:"description"`
	Visibility      string `json:"visibility"`
	Private         bool   `json:"private"`
	Archived        bool   `json:"archived"`
	Language        string `json:"language"`
	DefaultBranch   string `json:"default_branch"`
	StargazersCount int    `json:"stargazers_count"`
	ForksCount      int    `json:"forks_count"`
	URL             string `json:"html_url"`
	UpdatedAt       string `json:"updated_at"`
}

var repoSearch = searchSpec[apiRepo]{
	use:      "repos",
	short:    "Search for repositories",
	endpoint: "repositories",
	sorts:    []string{"forks", "help-wanted-issues", "stars", "updated"},
	fields: []string{
		"defaultBranch", "description", "forksCount", "fullName", "isArchived", "isPrivate",
		"language", "stargazersCount", "updatedAt", "url", "visibility",
	},
	record: func(r apiRepo) record {
		return record{
			"fullName":        r.FullName,
			"description":     r.Description,
			"visibility":      r.Visibility,
			"isPrivate":       r.Private,
			"isArchived":      r.Archived,
			"language":        r.Language,
			"defaultBranch":   r.DefaultBranch,
			"stargazersCount": r.StargazersCount,
			"forksCount":      r.ForksCount,
			"url":             r.URL,
			"updatedAt":       r.UpdatedAt,
		}
	},
	row: func(r apiRepo) []string {
		return []string{r.FullName, r.Description, r.Visibility, r.UpdatedAt}
	},
}

type apiSearchCommit struct {
	SHA    string `json:"sha"`
	URL    string `json:"html_url"`
	Commit struct {
		Message string          `json:"message"`
		Author  apiCommitAuthor `json:"author"`
	} `json:"commit"`
	Author     *apiUser `json:"author"`
	Repository apiRepo  `json:"repository"`
}

var commitSearch = searchSpec[apiSearchCommit]{
	use:      "commits",
	short:    "Search for commits",
	endpoint: "commits",
	sorts:    []string{"author-date", "committer-date"},
	fields:   []string{"author", "commit", "repository", "sha", "url"},
	record: func(c apiSearchCommit) record {
		return record{
			"sha":        c.SHA,
			"url":        c.URL,
			"repository": repoName(c.Repository.FullName),
			"author":     login(c.Author),
			"commit": record{
				"message": c.Commit.Message,
				"author":  record{"name": c.Commit.Author.Name, "date": c.Commit.Author.Date},
			},
		}
	},
	row: func(c apiSearchCommit) []string {
		headline, _, _ := strings.Cut(c.Commit.Message, "\n")
		return []string{c.Repository.FullName, c.SHA, headline, loginText(c.Author), c.Commit.Author.Date}
	},
	check: checkCommitQuery,
}

type apiCode struct {
	Path       string  `json:"path"`
	SHA        string  `json:"sha"`
	URL        string  `json:"html_url"`
	Repository apiRepo `json:"repository"`
}

var codeSearch = searchSpec[apiCode]{
	use:      "code",
	short:    "Search within code",
	endpoint: "code",
	fields:   []string{"path", "repository", "sha", "url"},
	record: func(c apiCode) record {
		return record{"path": c.Path, "repository": repoName(c.Repository.FullName), "sha": c.SHA, "url": c.URL}
	},
	row: func(c apiCode) []string { return []string{c.Repository.FullName, c.Path} },
}

var userSearch = searchSpec[apiUser]{
	use:      "users",
	short:    "Search for users and organizations",
	endpoint: "users",
	sorts:    []string{"followers", "joined", "repositories"},
	fields:   []string{"id", "login", "type", "url"},
	record: func(u apiUser) record {
		return record{"login": u.Login, "id": u.ID, "type": u.Type, "url": u.URL}
	},
	row: func(u apiUser) []string { return []string{u.Login, u.Type, u.URL} },
}

var (
	qualifierRE     = regexp.MustCompile(`^-?[A-Za-z][\w-]*:\S`)
	searchOperators = []string{"AND", "OR", "NOT"}
)

// checkCommitQuery rejects a commit query made only of qualifiers, such as
// "repo:owner/repo": the commit search API fails on those with an unhelpful
// validation error.
func checkCommitQuery(query string) error {
	words, ok := splitWords(query)
	if !ok {
		words = strings.Fields(query)
	}
	for _, w := range words {
		if !qualifierRE.MatchString(w) && !slices.Contains(searchOperators, w) {
			return nil
		}
	}
	return errors.New(`query needs at least one search term in addition to qualifiers, e.g. "fix repo:owner/repo" instead of "repo:owner/repo"`)
}

// splitWords splits s at whitespace outside double quotes, keeping the quotes,
// so that a quoted phrase such as "author:x y" is one word that doesn't look
// like a qualifier while author:"x y" still does. It reports false for an
// unterminated quote.
func splitWords(s string) ([]string, bool) {
	var (
		words   []string
		start   = -1
		inQuote bool
	)
	for i, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && unicode.IsSpace(r):
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if inQuote {
		return nil, false
	}
	if start >= 0 {
		words = append(words, s[start:])
	}
	return words, true
}
