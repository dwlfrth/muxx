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
- Three repo-local settings that a fresh clone needs again (they are not in git):
  - `git config remote.origin.pruneTags false`: the global `fetch.pruneTags`
    would otherwise delete upstream's tags on every fetch from GitLab, and
    without `v0.0.17` a build's version stamp becomes `v0.0.0-…`.
  - `git config remote.upstream.pruneTags false`: likewise, every fetch from
    upstream would delete this copy's own tags, which upstream does not have.
  - `git config push.followTags true`: a push takes along the annotated tags
    that point at pushed commits, so a new upstream release tag travels with
    the sync branch. A merge request cannot carry tags.

## Branches

- `main` is upstream plus the files only this copy has (`CLAUDE.md`,
  `.gitlab-ci.yml`). Changes reach it only through a merge request on GitLab.
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

## Settings outside git

These live in GitLab's and GitHub's settings and are not versioned; they are
the checklist when moving the project, e.g. to a self-hosted GitLab.

- GitLab, protected branch `main`: merge by Maintainers, push by no one, no
  force push.
- GitLab push mirror to GitHub: only protected branches, keep divergent refs.
  Authenticated with a GitHub fine-grained token `gitlab-muxx-push-mirror`
  (this repo only, Contents and Workflows read/write); the mirror stops when
  it expires. The mirror never deletes branches on GitHub: delete a merged
  feature branch there by hand, `git push git@github.com:dwlfrth/muxx.git
  --delete <branch>`.
- GitHub: default branch `main`. Never merge on GitHub; its `main` comes only
  from the mirror.
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

Locally, run those plus what upstream's maintainer runs before a release, which
neither CI runs: `make check` (on macOS, `GOOS=linux GOARCH=386 go vet ./...`
in place of its 386 vet, which needs cgo), `go test -race` on the touched
packages, and `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`.

## Do not "fix"

- Findings in upstream's code, e.g. staticcheck's SA1019 deprecations in
  `http/`: they are mjl's to change, and fixing them here makes every upstream
  update conflict.
