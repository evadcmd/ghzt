# ghzt

A cut-down `gh` and `git` that talk to the GitHub API instead of a local
checkout. See [#3](https://github.com/evadcmd/ghzt/issues/3) for the design
and roadmap.

This README only covers testing a local build against the real API for now.
The full README comes once the roadmap is done.

## Testing locally

### Build

```sh
go build -o bin/ghzt ./cmd/ghzt
```

`bin/` is gitignored. The binary picks its command tree by the name it is run
as (`filepath.Base(argv[0])`):

- `gh` → the gh tree, `git` → the git tree
- any other name (`ghzt`, `main`, ...) → the `ghzt` root, which has `gh` and
  `git` as subcommands, plus `commit-comment`, `review-reply` and
  `review-resolve`

So no symlinks are needed for testing:

```sh
bin/ghzt gh issue view 3 -R evadcmd/ghzt
bin/ghzt git ls-tree -r --name-only main -R evadcmd/ghzt
```

To try the busybox dispatch itself, symlink it and call the links by path:

```sh
ln -s ghzt bin/gh
ln -s ghzt bin/git
bin/git show main:go.mod -R evadcmd/ghzt
```

Don't put `bin/` on your `PATH`: the fake `git` would shadow the real one.

Errors are prefixed with the name the binary was run as, e.g. `ghzt: ...` or
`git: ...`.

### Credentials

`GHZT_TOKEN` wins if it is set. The quickest way to get one is gh's own token:

```sh
export GHZT_TOKEN=$(gh auth token)
```

Anything the commands write is then authored by your account.

To test as the GitHub App instead (writes are authored by the app's bot
user), leave `GHZT_TOKEN` unset and set:

- `GHZT_APP_ID`
- `GHZT_INSTALLATION_ID`
- exactly one of:
  - `GHZT_PRIVATE_KEY_PATH`: the PEM private key file
  - `GHZT_PRIVATE_KEY_SECRET`: the name or ARN of an AWS Secrets Manager
    secret holding the PEM. It is read with the default AWS credential chain,
    so `AWS_PROFILE` (or keys) and `AWS_REGION` need to be set too.

ghzt signs a JWT with the key and exchanges it for an installation token.

### Other environment

- `GHZT_ROOT`: the directory local files (`--body-file`, `--comments-file`,
  `-o`, the files `git commit` commits) are confined to. Defaults to the working directory. Paths are relative
  to it, or absolute inside it.
- `GH_DEBUG=api`: log every request and response to stderr, as gh does.
- Telemetry stays off unless an OTLP endpoint is set
  (`OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`).

### Things to try

Every repo-scoped command needs `-R owner/repo`. Reads are safe anywhere:

```sh
bin/ghzt gh issue view 3 -R evadcmd/ghzt --comments
bin/ghzt gh issue view 99999 -R evadcmd/ghzt   # HTTP 404: Not Found (GET ...)
bin/ghzt gh pr list -R evadcmd/ghzt --state all
bin/ghzt git show main:go.mod -R evadcmd/ghzt
bin/ghzt git show feat/some-branch:path/to/file -R evadcmd/ghzt
```

Writes change the real repo, so use a scratch issue or branch, and clean up
afterwards:

```sh
bin/ghzt gh issue create -R evadcmd/ghzt -t "ghzt test" -b "scratch"
bin/ghzt gh issue comment <n> -R evadcmd/ghzt -b "test"
bin/ghzt gh issue close <n> -R evadcmd/ghzt --reason not_planned
bin/ghzt git branch tmp-test -R evadcmd/ghzt         # from the default branch
bin/ghzt git branch tmp-test main -R evadcmd/ghzt    # again: HTTP 422
bin/ghzt git commit -R evadcmd/ghzt --branch tmp-test -m "test" README.md
```

ghzt can't delete branches; remove a test branch on the web or with the real
gh:

```sh
gh api -X DELETE repos/evadcmd/ghzt/git/refs/heads/tmp-test
```

`--help` works on every command, and notes where it differs from the real gh
or git. The full list of differences is in #3.

### Unit tests

```sh
go vet ./...
go test -race ./...
golangci-lint run
```

The tests run each command end to end through `cli.Run` against an
`httptest` server, so they need no network or credentials.
