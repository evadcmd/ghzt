# Testing against the real API

The README covers building, credentials and the unit tests. This page is the
longer walk through the commands against a real repo, for maintainers.

Every repo-scoped command needs `-R owner/repo`. The examples use
`bin/ghzt` from `go build -o bin/ghzt ./cmd/ghzt`.

## Busybox dispatch

The binary picks its command tree by the name it is run as
(`filepath.Base(argv[0])`):

- `gh` → the gh tree, `git` → the git tree
- any other name (`ghzt`, `main`, ...) → the `ghzt` root, which has `gh` and
  `git` as subcommands, plus `commit-comment`, `review-reply` and
  `review-resolve`

So no symlinks are needed for testing. To try the dispatch itself, symlink it
and call the links by path:

```sh
ln -s ghzt bin/gh
ln -s ghzt bin/git
bin/git show main:go.mod -R evadcmd/ghzt
```

Don't put `bin/` on your `PATH`: the fake `git` would shadow the real one.

Errors are prefixed with the name the binary was run as, e.g. `ghzt: ...` or
`git: ...`.

## Reads

Reads are safe anywhere:

```sh
bin/ghzt gh issue view 3 -R evadcmd/ghzt --comments
bin/ghzt gh issue view 99999 -R evadcmd/ghzt   # HTTP 404: Not Found (GET ...)
bin/ghzt gh pr list -R evadcmd/ghzt --state all
bin/ghzt git show main:go.mod -R evadcmd/ghzt
bin/ghzt git show feat/some-branch:path/to/file -R evadcmd/ghzt
```

## Writes

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

## Merge conflicts

To try a merge with a conflict, commit a different edit of README.md to a
second branch, then merge it:

```sh
bin/ghzt git branch tmp-test2 main -R evadcmd/ghzt
bin/ghzt git commit -R evadcmd/ghzt --branch tmp-test2 -m "test 2" README.md
bin/ghzt git merge -R evadcmd/ghzt --branch tmp-test tmp-test2   # lists README.md
bin/ghzt git merge -R evadcmd/ghzt --branch tmp-test --theirs README.md tmp-test2
```

## Cleaning up

ghzt can't delete branches; remove a test branch on the web or with the real
gh:

```sh
gh api -X DELETE repos/evadcmd/ghzt/git/refs/heads/tmp-test
gh api -X DELETE repos/evadcmd/ghzt/git/refs/heads/tmp-test2
```
