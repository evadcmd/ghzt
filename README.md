# ghzt

A cut-down `gh` and `git` that talk to the GitHub API instead of a local
checkout, built for an AI agent rather than a person.

## Why

ghzt exists to replace the GitHub MCP tools of taruza, a chat bot that works
on GitHub repos for its users. Those tools cost the agent context twice: every
tool schema sits in the prompt on every turn, and every call is a JSON shape
the model has to learn from that schema. The agent already knows `gh` and
`git`, so ghzt gives it those commands instead: it reuses what it knows, and
learns the few places ghzt differs from each command's `--help`.

- **Familiar commands, less context.** One binary, symlinked as `gh` and
  `git`, with the subcommands and flags the agent already uses. Where ghzt
  can't behave like the real CLI, `--help` says so.
- **API only, no checkout.** Every command is a GitHub API call. Reading a
  file is `git show main:go.mod`, and `git commit` builds a commit through
  the API from files in a local folder, with no index or working tree.
  Nothing is ever cloned or pushed.
- **A clean read/write split.** Read commands and write commands are separate
  command prefixes, so the host can allow reads and ask before writes with
  plain prefix rules. That's also why there is no `gh api`: it would let any
  write through a read prefix. The split only protects as much as the host's
  rules enforce it.
- **Output sized for a model.** Short human-readable text by default,
  gh-style `--json` where gh has it, one-line errors with a non-zero exit, an
  explicit `… truncated, N more` marker instead of silently cut output, and a
  `no … found` note on stderr instead of silently empty output.

The design and roadmap live in
[#3](https://github.com/evadcmd/ghzt/issues/3).

## Example

```sh
# Read a file from GitHub, without cloning
git show main:go.mod -R owner/repo

# Create a branch on GitHub
git branch feature-x main -R owner/repo

# Commit a local file to that branch
git commit -R owner/repo --branch feature-x -m "Update config" config.yaml
```

These commands run against GitHub, not a local repository. `config.yaml` is
read from `GHZT_ROOT` and committed at the same path in the repo; there is no
`git add` and no push, and no local Git repository is needed.

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
- `git commit --branch <b> -m <msg> [--rm <path>]... <path>...`: one commit
  on top of an existing branch, made through the API from files under
  `GHZT_ROOT`; no index, and the branch is never forced
- `git merge --branch <b> [--ours|--theirs <path>]... <head>`: a merge commit
  made through the API, not a local three-way merge; on a conflict nothing is
  written, and each conflicting path is resolved whole with `--ours` or
  `--theirs`

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

`bin/` is gitignored. Run as `ghzt`, the binary has `gh` and `git` as
subcommands, so no symlinks are needed for testing:

```sh
bin/ghzt gh issue view 3 -R evadcmd/ghzt
bin/ghzt git ls-tree -r --name-only main -R evadcmd/ghzt
```

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

Reads are safe anywhere:

```sh
bin/ghzt gh issue view 3 -R evadcmd/ghzt --comments
bin/ghzt git show main:go.mod -R evadcmd/ghzt
```

Writes change the real repo, so use a scratch branch, and delete it on the
web afterwards (ghzt can't delete branches):

```sh
bin/ghzt git branch tmp-test -R evadcmd/ghzt
bin/ghzt git commit -R evadcmd/ghzt --branch tmp-test -m "test" README.md
```

[docs/testing.md](docs/testing.md) walks through the rest: the busybox
dispatch, error cases, issue writes, and a merge with a conflict.

### Unit tests

```sh
go vet ./...
go test -race ./...
golangci-lint run
```

The tests run each command end to end through `cli.Run` against an
`httptest` server, so they need no network or credentials.
