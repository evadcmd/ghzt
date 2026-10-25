# ghzt

A cut-down `gh` and `git` that talk to the GitHub API instead of a local
checkout, built for an AI agent rather than a person.

## Why

ghzt exists to replace the GitHub MCP tools of taruza, a chat bot that works
on GitHub repos for its users. Those tools cost the agent context twice: every
tool schema sits in the prompt on every turn, and every call is a JSON shape
the model has to learn from that schema. The agent already knows `gh` and
`git`, so ghzt gives it those commands instead:

- **Familiar commands, less context.** One binary, symlinked as `gh` and
  `git`, with the subcommands and flags the agent already uses. Where ghzt
  can't behave like the real CLI, `--help` says so.
- **API only, no checkout.** Every command is a GitHub API call. Reading a
  file is `git show main:go.mod`, committing is `git commit --branch b` with
  files from a local folder, and nothing is ever cloned or pushed.
- **A clean read/write split.** Read commands and write commands are separate
  command prefixes, so the host can allow reads and ask before writes with
  plain prefix rules. That's also why there is no `gh api`: it would let any
  write through a read prefix.
- **Output sized for a model.** Short human-readable text by default,
  gh-style `--json` where gh has it, one-line errors with a non-zero exit, and
  an explicit `… truncated, N more` marker instead of silently cut output.

The design and roadmap live in
[#3](https://github.com/evadcmd/ghzt/issues/3).

## Design

- **Busybox dispatch.** The command tree is picked by `argv[0]`: `gh`, `git`,
  or `ghzt` for everything else.
- **No implicit repo.** Every repo-scoped command needs `-R owner/repo`;
  there is no local checkout to infer it from.
- **Auth from the environment.** `GHZT_TOKEN`, minted by the host, or a
  GitHub App ID and private key as the fallback.
- **Confined local files.** Every local read or write (`--body-file`, `-o`,
  the files `git commit` commits) goes through `os.Root` under `GHZT_ROOT`.
  The folder is chosen by the environment, never by a command argument, and
  symlinks are refused.
- **Safe writes.** Branch updates are never forced, so a branch that moved
  meanwhile isn't clobbered. `git commit` also checks every SHA taken from a
  response before it goes into the next request.

## Commands

`gh`:

- `gh issue view|create|comment|close|edit`
- `gh pr view|list|create|edit|comment|review`
- `gh label list|create`
- `gh search issues|prs|repos|commits|code|users`

`git`:

- `git show <commit>` or `git show <ref>:<path>`: a commit's diff, or a
  file's content
- `git ls-tree [-r] [--name-only] <ref> [<path>...]`
- `git branch <new> [<start-point>]`: create a branch (create only)
- `git commit --branch <b> -m <msg> [--rm <path>]... <path>...`: commit
  files from `GHZT_ROOT`
- `git merge --branch <b> [--ours|--theirs <path>]... <head>`: merge, with
  per-path conflict resolution

`ghzt` only, since gh has no such commands:

- `ghzt commit-comment <sha>`
- `ghzt review-reply <pr> <comment-id>`
- `ghzt review-resolve <pr> <comment-id>`

`--help` on each command lists its flags and where it differs from the real
gh or git. The full list of differences is in #3.

## Status

All the Go-side roadmap steps are merged. What's left is on the taruza side
(installing the binary, the permission rules, and cutting over from the MCP
tools) and a few live API checks; #3 tracks both.

## Releasing

Pushing a `v*` tag builds an image holding only the binary, for
`linux/amd64` and `linux/arm64`, and pushes it to
`ghcr.io/evadcmd/ghzt:<tag>` (`.github/workflows/release.yml`). Other images
copy the binary out of it:

```dockerfile
COPY --from=ghcr.io/evadcmd/ghzt:v0.1.0 /ghzt /usr/local/bin/ghzt
```

To try an unreleased build, build the image locally and copy from that tag
instead:

```sh
docker build -t ghzt:dev .
```

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
  `-o`, the files `git commit` commits) are confined to. Defaults to the
  working directory. Paths are relative to it, or absolute inside it.
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

To try a merge with a conflict, commit a different edit of README.md to a
second branch, then merge it:

```sh
bin/ghzt git branch tmp-test2 main -R evadcmd/ghzt
bin/ghzt git commit -R evadcmd/ghzt --branch tmp-test2 -m "test 2" README.md
bin/ghzt git merge -R evadcmd/ghzt --branch tmp-test tmp-test2   # lists README.md
bin/ghzt git merge -R evadcmd/ghzt --branch tmp-test --theirs README.md tmp-test2
```

ghzt can't delete branches; remove a test branch on the web or with the real
gh:

```sh
gh api -X DELETE repos/evadcmd/ghzt/git/refs/heads/tmp-test
gh api -X DELETE repos/evadcmd/ghzt/git/refs/heads/tmp-test2
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
