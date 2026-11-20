# muxx

A copy of [mox](https://github.com/mjl-/mox), developed in a private GitLab
project and published to <https://github.com/dwlfrth/muxx>. Upstream code is
mjl's; this file covers only what is specific to this copy. It is public: no
hostnames, addresses or deployment details here. Anything about the machines
that run muxx lives in the private `dotfiles` repo.

## Remotes

- `origin` fetches from the GitLab project and has **two push URLs**, GitLab
  and GitHub, so every push goes to both. Nothing private belongs in this repo.
  GitLab push options such as `-o merge_request.create` work through it: GitHub
  accepts and ignores them.
- `upstream` is `https://github.com/mjl-/mox.git`, fetch only (push URL
  `DISABLED`).
- Repo-local settings that a fresh clone needs again (they are not in git):
  - `git config remote.origin.pruneTags false`: the global `fetch.pruneTags`
    would otherwise delete upstream's tags on every fetch from GitLab, and
    without `v0.0.17` a build's version stamp becomes `v0.0.0-…`.
  - `git config remote.upstream.pruneTags false`: likewise, every fetch from
    upstream would delete this copy's own tags, which upstream does not have.
  - `git config push.followTags true`: a push takes along the annotated tags
    that point at pushed commits, so a new upstream release tag travels with
    the sync branch. A merge request cannot carry tags.
  - `git config rerere.enabled true` and `git config rerere.autoUpdate true`:
    git records how a conflict was resolved and applies it again when the same
    conflict comes back, e.g. in the few lines muxx changes for its name.
  - `echo tmp/ >> .git/info/exclude`: the gates run tests with
    `TMPDIR=$PWD/tmp`, and upstream's `.gitignore` does not cover it.

## Branches

- `main` is upstream plus the files only this copy has (`CLAUDE.md`,
  `MUXX.md`, `.github/README.md`, `.gitlab-ci.yml`, `Dockerfile.muxx` and its
  `.dockerignore`, `.github/workflows/muxx-image.yml`). Changes reach it only
  through a merge request on GitLab. GitHub shows `.github/README.md` on the
  repository page instead of upstream's `README.md`, which stays as it is.
- Each feature branches from **upstream's** `main`, not from ours, so it can
  become a pull request to mjl-/mox without carrying other features or the
  muxx-only files.
- Upstream updates:
  ```sh
  git fetch upstream --tags
  git switch -c sync-upstream-YYYYMMDD upstream/main
  git push -u origin sync-upstream-YYYYMMDD -o merge_request.create -o merge_request.target=main
  ```
  then merge the request on GitLab (a merge commit, no squash).

## Versions and name

The version scheme and the build command are in `MUXX.md`; it is their only
description. To tag a release on the merged commit on `main`, with the release
note as message, and publish only the tag (to both remotes):
```sh
tag=muxx-$(date -u +%Y%m%d%H%M) && git tag -a "$tag" && git push origin "$tag"
```
`push.followTags` does not help here: `main` itself is never pushed by hand.
Push the tag only once GitHub's `main` has the merge commit, which arrives
through the mirror: `git ls-remote git@github.com:dwlfrth/muxx.git main` must
show it. The image workflow refuses a tag whose commit is not on `main`.

`.github/workflows/muxx-image.yml` builds the image on GitHub: on a release tag
it checks that the tag is annotated, named `muxx-YYYYMMDDHHMM` and on a commit
made on `main` itself (a merge commit, not one of a merged branch), and that
the release was not pushed before (a moved tag must not replace an image),
builds for linux/amd64, checks the version of the build (release named, no
local changes), and pushes it to `ghcr.io/dwlfrth/muxx:<release>`, never
`latest`. On `main`, when the image files change, it only builds. Only
linux/amd64 is built, the platform of the servers that run muxx: arm64 was
built at first, under emulation, and took most of each run's time for a
platform nothing runs. `Dockerfile.muxx` builds with the Go version CI tests
with (`golang:1.27-alpine`): bump it together with CI's 1.27. The workflow's
actions are pinned by commit, with the version as comment; bump them by hand.
`Dockerfile.muxx.dockerignore` keeps `.git` in the build context for the
version stamp: excluding a tracked file there makes every image report local
changes.

A build of muxx from a commit without a release tag sets the variable to
`muxx-dev`, so it reads `muxx dev (mox <version>)` and cannot be mistaken for
a release; the command is in `MUXX.md`. Builds without the variable stay plain
mox, which upstream's tests and the generated documentation rely on.

Everything muxx calls itself derives from one build variable,
`moxvar.Distribution`, in `moxvar/distribution.go`: without it, a build is plain
mox. `moxvar.Rename` renames text as written in the code, never text that
contains data (paths, hostnames, passwords with the word mox would change
too). It is applied centrally to help texts, flag descriptions (not their
default values) and log messages, so new texts from upstream there follow
automatically; to the comments of the configuration files quickstart writes;
and to quickstart's and a few other printed texts individually, as format
strings before formatting. Go's standard `log` output is not renamed: its
messages carry paths and errors. The web interfaces' page titles are renamed
where they are served (`moxvar.RenameTitle`), their texts through the helpers
in `lib.ts`, including `X-Mox-` header names in fixed examples
(`productHeaders`). A new `X-Mox-` header or `"mox/"` User-Agent should use
`moxvar.HeaderPrefix` and `moxvar.UserAgent`. Generated
documentation (`doc.go`, `config/doc.go`) stays mox: CI regenerates it without
the variable.

## Settings outside git

These live in GitLab's and GitHub's settings and are not versioned; they are
the checklist when moving the project, e.g. to a self-hosted GitLab.

- GitLab, protected branch `main`: merge by Maintainers, push by no one, no
  force push.
- GitLab push mirror to GitHub: only protected branches, keep divergent refs.
  Authenticated with a GitHub fine-grained token `gitlab-muxx-push-mirror`
  (this repo only, Contents and Workflows read/write); the mirror stops when
  it expires. Tags are mirrored too, so a release tag pushed to GitLab can
  reach GitHub before the second push URL does. The mirror never deletes
  branches on GitHub: delete a merged feature branch there by hand,
  `git push git@github.com:dwlfrth/muxx.git --delete <branch>`.
- GitHub: default branch `main`. Never merge on GitHub; its `main` comes only
  from the mirror.
- GitHub package `ghcr.io/dwlfrth/muxx`: created by the first release, linked
  to the repository by the image's source label. Its visibility is separate
  from the repository's: the package started private and was made public by
  hand, which a new package would need again.
- GitHub ruleset `main: no deletion or force push` on `main`, with an empty
  bypass list: it blocks deleting `main` and rewriting its history, and lets
  normal pushes through, so the mirror is unaffected. A rule that blocks pushes
  (requiring a pull request, restricting updates) would block the mirror too:
  its token acts as the account that owns it, so a bypass for the mirror would
  exempt that account entirely.

## Gates

CI runs the same steps as upstream's `.github/workflows/build-test.yml`, on Go
stable and oldstable with Node 24: `make build` (after `touch */*.ts`, so the
frontend is rebuilt), `TMPDIR=$PWD/tmp make test`, `make fmt` and `git diff
--exit-code`. GitLab runs them in the `gates` job of `.gitlab-ci.yml`, and
GitHub Actions runs upstream's workflow on every push to GitHub.

GitLab's `checks` job adds, blocking, on Go 1.27: `go vet` (also for
linux/386), `ineffassign` v0.1.0, `staticcheck` v0.8.2 without SA1019 (see "Do
not fix"), `govulncheck` v1.10.0, and `actionlint` v1.7.12 with Debian's
`shellcheck` on `.github/workflows/muxx-image.yml` (not on upstream's
workflow). Those versions are pinned there and bumped by hand; run them
locally at the same versions with `go run ...@<pin>`.
GitLab's `race` job runs `make test-race`, the whole suite under the race
detector, on Go 1.27; locally, `go test -race` on the touched packages is
enough before pushing. Do not run other gates while a test run with
`TMPDIR=$PWD/tmp` is going: `./...` then includes its temporary package copies
in `tmp/` and fails with errors that are not real.

## Do not "fix"

- Findings in upstream's code, e.g. staticcheck's SA1019 deprecations in
  `http/`: they are mjl's to change, and fixing them here makes every upstream
  update conflict.
