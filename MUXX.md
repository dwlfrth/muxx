# muxx

muxx is [mox](https://github.com/mjl-/mox), the modern full-featured mail
server by Mechiel Lukkien, with additional features. Everything else is
upstream mox, unchanged: its documentation, configuration and behaviour apply
as they are. See [README.md](README.md).

## Features

Each feature is off unless configured. The release linked is the first that
has the feature, and its release note has the details; the newest feature
links the latest release.

| Feature | Setting | Release |
|---|---|---|
| Send-only domains: a domain that only sends email through this server, and never receives it here | `SendOnly` in a domain in `domains.conf` | [muxx-202611210226](https://github.com/dwlfrth/muxx/releases/tag/muxx-202611210226) |
| Leave the server's own private IP address out of the Received header | `HidePrivateReceivedIP` in `muxx.conf` | [muxx-202611211643](https://github.com/dwlfrth/muxx/releases/tag/muxx-202611211643) |
| Spam filtering and training with rspamd, see [rspamd/README.md](rspamd/README.md) | `Rspamd` section in `muxx.conf` | [muxx-202611232229](https://github.com/dwlfrth/muxx/releases/tag/muxx-202611232229) |
| rspamd checks without password, so rspamd's greylisting, rate limits, neural network and reputation apply | `CheckURL` in the `Rspamd` section | [latest](https://github.com/dwlfrth/muxx/releases/latest) |

Every release, since the first,
[muxx-202611201932](https://github.com/dwlfrth/muxx/releases/tag/muxx-202611201932),
presents itself as muxx (see "Relation to mox") and is published as a
container image (see "Image").

## The name

muxx keeps its roots in mox: the same letters, one vowel apart, and the
second x for mox extended. The rest is a wink to mutt, the mail client whose
motto, "All mail clients suck. This one just sucks less.", every mail server
could borrow.

## Relation to mox

Every muxx feature is off unless configured, so muxx without configuration
behaves exactly like mox. Each feature is developed on its own branch from
upstream's main, so it can be proposed to mox upstream on its own.

A muxx release build presents itself as muxx: in its version, the SMTP
greeting, the IMAP ID, the HTTP User-Agent, the command line, help texts and
log messages, the titles of the web interfaces, its own message and webhook
headers (`X-Muxx-*`), its metrics (`muxx_*`), the default configuration file
(`muxx.conf`), the service files it generates (`muxx.service`,
`muxx.freebsd.rc`), the environment variables `MUXXCONF` and `MUXX_DOCKER`,
and the default system user and home directory (`muxx`, `/home/muxx`).

Internally it is still mox: the Go module, `domains.conf`, internal
environment variables, links to mox, and the documentation generated into the
repository keep their names. A build without a release tag is mox in every
respect.

## Keeping up with mox

muxx follows mox's main branch: upstream changes are merged into muxx as
they appear, through the same review as any other change, and upstream
release tags are carried over. The release note of each muxx tag names the
mox version it is based on, so a release's distance from upstream can always
be checked against mox's own releases and security announcements.

## Versions

Releases are annotated git tags `muxx-YYYYMMDDHHMM`, in UTC, e.g.
`muxx-202611191730`. The tag message is the release note: the mox version the
release is based on, and the muxx features it contains. Each release also
has a page on the Releases of GitHub and GitLab, made from that note.

A release build shows its release and the mox version it is based on, e.g.
`muxx 202611191730 (mox v0.0.18-0.20261119…-ac22e02)`. The update check and
the links to mox's documentation keep using the plain mox version.

## Building

As mox, see [README.md](README.md). A release build is made from a commit
with a muxx tag, and carries that tag:

    tag=$(git describe --tags --exact-match --match 'muxx-*') &&
      go build -o muxx -ldflags "-X github.com/mjl-/mox/moxvar.Distribution=$tag"

On a commit without a muxx tag, `git describe` fails and nothing is built. A
binary built with a name other than a lower case name of letters and digits,
optionally followed by a dash and a release, refuses to start.

A development build, from any commit, is marked as such instead of as a
release, e.g. `muxx dev (mox v0.0.18-…-531ebb8…)`:

    go build -o muxx -ldflags "-X github.com/mjl-/mox/moxvar.Distribution=muxx-dev"

## Image

Each release is published as a container image for linux/amd64, named after the
release, e.g. `ghcr.io/dwlfrth/muxx:202611191730`. There is no `latest`: choose
a release. The image runs `muxx serve` in `/muxx`, so it expects its
configuration in `/muxx/config` and, with the default configuration, its data
in `/muxx/data`.

It is built from `Dockerfile.muxx`, a copy of mox's `Dockerfile`, when a
release tag is pushed. Its standard OCI labels and annotations name the
release, the commit and the license; its build provenance attestation records
the base image by digest. To build it yourself, from a commit with a muxx tag:

    tag=$(git describe --tags --exact-match --match 'muxx-*') &&
      docker build -f Dockerfile.muxx --build-arg DISTRIBUTION=$tag -t muxx:${tag#muxx-} .
Without the `-ldflags` setting, the result is a plain mox build.

## Source

Developed in a private repository and published at
<https://github.com/dwlfrth/muxx>. Changes to mox itself belong upstream at
<https://github.com/mjl-/mox>.

## License

Like mox, available under the MIT license ([LICENSE.MIT](LICENSE.MIT)), with
the Public Suffix List under the Mozilla Public License 2.0
([LICENSE.MPLv2.0](LICENSE.MPLv2.0)). muxx's changes to mox are under the same
MIT license ([LICENSE.muxx](LICENSE.muxx)). This applies to all muxx
releases and images, including those published before `LICENSE.muxx` was
added. The `licenses` link in the web interfaces lists mox's and its
dependencies' licenses, as in mox; it does not include `LICENSE.muxx`, so
muxx changes nothing in upstream's code for it.
