# rspamd integration

muxx can use an [rspamd](https://rspamd.com) server to judge incoming mail and
trains it with what users mark as junk or not junk. This document explains how
the integration behaves, how to configure both sides, and why it was built the
way it is. The configuration reference itself is the `Rspamd` section of
`mox.conf` (see `config/doc.go`); this document does not replace it.

Without an `Rspamd` section in `mox.conf`, nothing changes: muxx behaves
exactly like mox, and none of the code below runs.

Requires rspamd 4.1.1 or later.

## Contents

- [In short](#in-short)
- [How a delivery is decided](#how-a-delivery-is-decided)
- [Training](#training)
- [Configuration](#configuration)
- [Compared with rspamd's usual deployment](#compared-with-rspamds-usual-deployment)
- [Observing it](#observing-it)
- [Design decisions](#design-decisions)
- [What testing changed](#what-testing-changed)
- [Not included](#not-included)
- [How it was built and tested](#how-it-was-built-and-tested)
- [Code map](#code-map)

## In short

- Configured rules of the account come first and last; rspamd decides in
  between. mox's own filters (reputation, Bayes junk filter, DNSBLs and the
  heuristics around them) are replaced by rspamd's verdict, so a message never
  gets two verdicts.
- rspamd's action decides delivery: deliver, deliver to Junk, reject, or ask
  the sender to try again later.
- If rspamd cannot be used, mox's own checks decide, as without rspamd.
- Training follows the account's own junk filter: whenever a message becomes
  junk or not junk for muxx, rspamd learns the same. Training also stays in
  muxx, so removing rspamd later loses nothing.

## How a delivery is decided

For each delivered address, in order:

| Step | What | Who decides |
|---|---|---|
| 1 | Delivery rate limits | mox, unchanged |
| 2 | A ruleset's `ListAllowDomain` (validated mailing list): accepted | mox, unchanged |
| 3 | DMARC `p=reject`: rejected, except for rulesets with `IsForward` | mox, unchanged |
| 4 | A destination's `MessageAuthRequiredSMTPError` | mox, unchanged |
| 5 | Messages to DMARC and TLS reporting addresses are processed without rspamd | mox, unchanged |
| 6 | The DMARC-validated From address is one the account has sent to in the past 3 years: accepted without rspamd | muxx, reusing mox's recipient records |
| 7 | The subject has a valid subjectpass token: accepted without rspamd (the Introbox still applies) | muxx, reusing mox's subjectpass |
| 8 | rspamd checks the message and its action is applied (below) | rspamd |

What rspamd's verdict replaces: mox's reputation-based accept and reject, the
first-time sender delay, the junk filter's classification, the DNSBL checks,
and the SPF-fail and reverse-DNS heuristics. mox's reputation is still used
for two things only: to choose the Introbox, and to decide whether a rejected
sender gets a subjectpass hint.

### rspamd's actions

| rspamd action | muxx | SMTP reply | Reason value |
|---|---|---|---|
| no action | Delivered to the normal mailbox: the destination's, or a matching ruleset's. Unknown senders go to the Introbox if the account has one. | 250 | `rspamd-accept` |
| add header, rewrite subject | Delivered to the mailbox with the Junk special-use role, with `$Junk` set. The message itself is not changed: no subject rewrite, no `X-Spam` header. Also when a ruleset matches (see [What testing changed](#what-testing-changed)). | 250 | `rspamd-junk` |
| reject | Rejected. A copy is kept in the Rejects mailbox, as mox does for its own rejects. A ruleset's `AcceptRejectsToMailbox` accepts it there instead. A first-time sender with DMARC pass, to an account with SubjectPass, gets a subjectpass token in the reply, as with mox's own checks. | 550 5.7.1 | `rspamd-reject` (`give-subjectpass` with the hint) |
| greylist, soft reject | Refused temporarily; the sender tries again. A copy is kept in Rejects and removed when the message is delivered later. `AcceptRejectsToMailbox` never applies: the sender must retry. | 450 4.7.0 | `rspamd-tempreject` |

Special cases:

- **Skipped with a verdict.** rspamd may skip its full checks and still give a
  verdict, e.g. `reject` for the GTUBE test pattern. The verdict is applied.
- **Skipped without a verdict, an unknown action, or an error** (rspamd
  unreachable, too old, timeout, refusing the message size, a malformed
  answer): the error is logged and mox's own checks decide, as without
  rspamd. The reason header only says rspamd was unavailable; details such as
  rspamd's URL stay in the log.
- **No Junk mailbox.** If the account has no mailbox with the Junk role, one is
  created, named as configured for Junk in `InitialMailboxes`, or `Junk`. An
  existing mailbox with that name gets the role back. With
  `NoJunkMailboxCreate`, the message goes to the normal mailbox with `$Junk`
  instead; note that with mox's default automatic junk flags, Inbox is neutral
  and clears the flag, so the verdict is then only visible in the reason
  header.

### What rspamd receives

Each check is a `POST /checkv3` with:

- the connecting IP, the EHLO name (or address literal such as `[192.0.2.1]`),
  the verified reverse DNS name, the SMTP `MAIL FROM` and the one recipient;
- muxx's queue ID, for matching rspamd's logs with muxx's;
- `deliver_to`: the delivered address without catchall suffix (`user+tag` is
  sent as `user`), so rspamd can keep per-user statistics;
- the message with muxx's own `Received` header in front, as it will be stored
  (RFC 5321 section 4.4).

Recipients are checked one after another, each with its own `deliver_to`. Once
a check fails, the remaining recipients of the message are not checked again.
A sender waits up to 10 minutes for the reply to the message (RFC 5321 section
4.5.3.2.6), so after 5 minutes of checks the remaining recipients get the most
recent result, noted in their reason header without naming the earlier
recipient (it may be a Bcc).

## Training

### The rule

rspamd learns exactly when the account's junk filter changes its mind about a
message:

- a message that gets only `$Junk` is learned as spam;
- a message that gets only `$NotJunk` is learned as ham;
- a message with neither or both flags is not trained.

The flags change in three ways, all of which count:

1. a mail client or the webmail sets or clears them;
2. a move or copy sets them: always `$Junk` into the mailbox with the Junk
   role, and per the account's `AutomaticJunkFlags` for other mailboxes;
3. replying to or forwarding a message in the webmail sets `$NotJunk` on it
   when it had neither flag.

One addition, only with rspamd configured: a message moved or copied **out of
Junk into a neutral mailbox**, e.g. Inbox with mox's default automatic junk
flags, also gets `$NotJunk`. Taking a message out of Junk is the usual way to
say "not junk", and with the default flags it would otherwise clear `$Junk`
and train nothing. Moving to the mailbox with the Trash role is excluded:
deleting is not rescuing.

With mox's default automatic junk flags (junk: `^(junk|spam)`, neutral:
`^(inbox|introbox|neutral|postmaster|dmarc|tlsrpt|rejects)`, everything else
not junk):

| User action | Learned (muxx and rspamd) |
|---|---|
| Inbox → Junk | spam |
| Junk → Inbox | ham (the rescue rule) |
| Junk → Archive | ham |
| Junk → Trash | ham (Trash is "not junk" by default; see below) |
| Inbox → Archive | ham |
| Inbox → Trash | ham |
| Set `$Junk` / `$NotJunk` in a client | spam / ham |
| Reply in the webmail | ham, if the message had neither flag |

### Keeping Junk → Trash as spam

To keep a message moved from Junk to Trash as spam, set all three regular
expressions of `AutomaticJunkFlags`, with Trash in none of them. A mailbox
that matches none of the three keeps the flags of a moved message:

```
AutomaticJunkFlags:
	Enabled: true
	JunkMailboxRegexp: ^(junk|spam)
	NeutralMailboxRegexp: ^(inbox|introbox|neutral|postmaster|dmarc|tlsrpt|rejects)
	NotJunkMailboxRegexp: ^(archive|sent)
```

Only the mailboxes in `NotJunkMailboxRegexp` then make a message not junk, so
list every mailbox that should. Do not add Trash to the neutral expression
instead: neutral clears both flags, so the junk filter forgets the message
while rspamd keeps it as spam.

### What is not learned

- **Added messages.** Delivery, IMAP `APPEND` and imports never train rspamd.
  A delivered message carries rspamd's own verdict, and rspamd must not learn
  from itself.
- **Forgetting.** rspamd has no way to forget a message. When the junk filter
  untrains one (both or neither flag, or the message is removed), rspamd keeps
  what it learned. A message later marked the other way is relearned as that
  class.
- **Training done in rspamd itself**, e.g. with `rspamc` or rspamd's web
  interface, never reaches muxx.

muxx's own junk filter, on the other hand, does learn from deliveries, as mox
always does for any message added with `$Junk`. With rspamd, that includes
every message rspamd delivered to Junk. This keeps rspamd's verdicts in muxx
for when rspamd is removed; taking such a message out of Junk retrains it as
ham.

### How training is sent

- Only after the change is saved, so a change that is rolled back trains
  nothing.
- In the background, one message at a time, so a slow or unreachable rspamd
  never slows down a mail client.
- Best effort: up to 1000 messages wait in a queue in memory. More are dropped
  and counted, and queued messages are lost when muxx stops.
- To `/learnspam` or `/learnham`, with the message as received (without the
  headers muxx adds), and the user for per-user statistics taken from the
  stored `Delivered-To` header (RFC 9228), so it matches the check. Messages
  without that header, e.g. imported ones, are sent without a user and rspamd
  makes its own choice.
- rspamd may decline: "already learned" and "refused" (rspamd's own learning
  conditions, e.g. fewer than its minimum number of tokens) are counted
  separately and are not errors.

## Configuration

### muxx

In `mox.conf`:

```
Rspamd:
	ControllerURL: http://localhost:11334
	Password: read-only-password
	EnablePassword: enable-password
```

| Option | Default | Meaning |
|---|---|---|
| `ControllerURL` | required | Base URL of rspamd's controller. Checks and training both go to the controller. http or https, without credentials, query string or fragment. Also linked from the admin page as rspamd's web interface, so it should be reachable from the admin's browser for that link to work. Named `URL` before 2026-11-25: a configuration with `URL` is refused until it is renamed. |
| `Password` | empty | The controller's `password`, sent with checks and status requests. |
| `EnablePassword` | `Password` | The controller's `enable_password`, sent with training. rspamd only accepts training with `enable_password` when that is set. |
| `Timeout` | 16s | Per request. Twice rspamd's default `task_timeout` of 8s, after which rspamd finishes with what it has and still answers. |
| `NoJunkMailboxCreate` | off | Do not create a Junk mailbox for junk verdicts; see above. |

For https, the certificate authorities from muxx's TLS configuration (`TLS.CA`)
are trusted, as for other HTTPS requests by mox, or else the system's. There is
no option to disable certificate verification.

Requests go through a proxy if muxx's environment sets one (`HTTP_PROXY`,
`HTTPS_PROXY`, `NO_PROXY`), as for Go programs generally, for setups where
rspamd is not reached directly. Requests to loopback addresses never use a
proxy. A proxy sees the `Password` header and the messages unless the URL is
https.

### rspamd

The integration works with rspamd's shipped defaults. Points worth knowing:

- **Access.** The controller has two passwords: `password` for read-only
  commands such as checks, `enable_password` for privileged ones such as
  training. Alternatively, `secure_ip` grants full access without a password
  to listed addresses. Keep `secure_ip` to what needs it: an address that
  later belongs to another host gets the same access. `secure_ip` encrypts
  nothing. See rspamd's
  [controller worker](https://docs.rspamd.com/workers/controller).
- **Transport.** Across an untrusted network, use https (the controller serves
  TLS natively since rspamd 4.0): it is the only option that protects the
  messages and the password. On the same machine or a trusted network, http is
  fine.
- **Redis.** rspamd's greylisting and rate limits need Redis, and its Bayes
  classifier uses Redis as its default backend. Without Redis there is no
  greylisting. See rspamd's
  [Redis configuration](https://docs.rspamd.com/configuration/redis).
- **Bayes.** rspamd only uses its classifier once both spam and ham have
  `min_learns` learned messages (200 by default). Until then the admin page
  shows the progress. Per-user statistics need `per_user` on the classifier;
  muxx always sends the user, so turning it on later needs no muxx change.
  See rspamd's
  [statistics settings](https://docs.rspamd.com/configuration/statistic).
- **Autolearn** is off by default in rspamd, and muxx does not need it. If you
  turn it on, rspamd learns from its own verdicts at delivery. Also in the
  statistics settings.
- **Relays and forwarders.** rspamd judges the connecting IP. For mail that
  arrives through a relay or a forwarding service, that is the relay's or
  forwarder's IP, so IP-based checks (SPF, DNS blocklists, IP reputation) look
  at the wrong host. rspamd's `external_relay` module (off by default) can take
  the original sender's IP from the relay's `Received` header. A muxx ruleset
  with `IsForward` only affects mox's own checks and the DMARC reject policy,
  not rspamd. See rspamd's
  [external_relay module](https://docs.rspamd.com/modules/external_relay).
- **Local addresses.** rspamd skips several checks (e.g. SPF, greylisting,
  DNSBLs) for IPs in its `local_addrs`, private ranges by default. If muxx sees
  incoming mail from private addresses, e.g. behind a proxy, configure
  `external_relay` or `local_addrs` accordingly.
- **Version.** The controller serves `/checkv3` since rspamd 4.1.1. With an
  older one, checks fall back to mox's own, and the log and admin page say
  "rspamd 4.1.1 or later is required".

### Example rspamd configuration

A working set of files, as used with muxx in production, with placeholders for
the secrets and host names. They go in rspamd's `local.d` directory
(`/etc/rspamd/local.d` in rspamd's container image); everything else stays at
rspamd's defaults. Keep them readable for rspamd only, e.g. owner root, group
rspamd, mode 0640.

`worker-controller.inc`: two different passwords, stored hashed, and
password-less access only from the machine itself.

```
password = "<output of: rspamadm pw, for the read-only password>";
enable_password = "<output of: rspamadm pw, for the enable password>";
allow_file_and_shm_inputs = false;
secure_ip = ["127.0.0.1", "::1"];
```

muxx's `Password` and `EnablePassword` take the two passwords themselves, not
the hashes. `allow_file_and_shm_inputs = false` refuses requests that ask rspamd
to read a message from a local file or shared memory instead of the request
body; muxx always sends the message in the request.

`worker-normal.inc` and `worker-proxy.inc`, the same refusal for the other
workers that accept messages:

```
allow_file_and_shm_inputs = false;
```

`redis.conf`, for greylisting, rate limits and Bayes:

```
servers = "<redis host>:6379";
password = "<redis password>";
timeout = 1s;
db = "0";
```

The Redis server, in its own `redis.conf`, with a password for the default
user and without the commands that can wipe or reconfigure it. Persistence keeps
the Bayes data across restarts, and the memory limit evicts only keys with an
expiry, so Bayes tokens are not evicted:

```
user default on ><redis password> ~* &* +@all -flushall -flushdb -config -debug -shutdown -module -replicaof -slaveof -acl -failover -monitor
dir /data
save 3600 1 300 100 60 10000
appendonly yes
appendfsync everysec
maxmemory 512mb
maxmemory-policy volatile-ttl
```

Check the rspamd configuration with `rspamadm configtest` before restarting
rspamd.

## Compared with rspamd's usual deployment

rspamd is usually deployed with Postfix (milter) for checking and Dovecot
(IMAPSieve) for training. muxx is both the MTA and the IMAP server, so it does
both itself:

| | Usual deployment | muxx |
|---|---|---|
| Checking | milter, once per message | HTTP `/checkv3`, once per recipient |
| Junk (add header) | adds `X-Spam` headers; a Sieve rule files it in Junk | files it in Junk with `$Junk`; verdict in muxx's reason header |
| Reject | 554 5.7.1 | 550 5.7.1 |
| Greylist / soft reject | 451 4.7.1 | 450 4.7.0 |
| rspamd unavailable | the MTA's milter default (tempfail or accept) | mox's own checks decide |
| Training | Sieve scripts on moves into and out of Junk, except to Trash | the junk filter's flag rule, which also covers moves, explicit flags and webmail replies |
| Known correspondents | rspamd's `replies` module, for replies to mail sent through rspamd | accepted before rspamd if DMARC passes (mox's recipient records) |

On the reply codes: RFC 6647 section 5 names 450 as the reasonable choice for
greylisting when the connection stays open, and RFC 3463 section 3.8 defines
X.7.1 as "useful only as a permanent error", so muxx uses 4.7.0 for temporary
refusals.

## Observing it

- **Reason header.** Every delivered message gets muxx's reason header
  (`X-Mox-Reason`; `X-Muxx-Reason` in muxx builds) with the decision
  (`rspamd-accept`, `rspamd-junk`, `msgtofull` for a known correspondent,
  `subjectpass`, ...), rspamd's action, score, required score and up to 10
  symbols, largest contribution first. It also notes a reused result, a
  fallback to mox's checks, or the Introbox.
- **Admin page.** The index shows one line, linking to rspamd's web interface:
  `Rspamd (version 4.3.0, training queue 0) - Bayes in training: spam 13 of 200
  (187 remaining), ham ready`. The Bayes part shows only while a class is below
  `min_learns`, and says "all users" when the classifier is per user. An
  unreachable or too old rspamd is shown in red.
- **Metrics,** registered only with rspamd configured (named `muxx_rspamd_*` in
  muxx builds):
  - `mox_rspamd_check_total{result}`: the action applied, `fallback`, or
    `reused`;
  - `mox_rspamd_check_duration_seconds`;
  - `mox_rspamd_learn_total{result}`: `ok`, `already`, `refused`, `failed`,
    `dropped`;
  - `mox_rspamd_learn_queue_length`.
- **Logs.** `rspamd analyzed` with action and score per check; `trained rspamd`
  per learned message at debug level, failures at error level. Each check
  sends muxx's queue ID to rspamd, for matching the two sides.

## Design decisions

The integration was designed from mox's and rspamd's own defaults, kept
minimal, and reuses mox's mechanisms wherever one exists. An earlier prototype
served only as a source of lessons, never as a reason to keep something.

- **HTTP, not milter.** rspamd's HTTP API is enough for an MTA that is its own
  milter host; generic milter support would be a separate feature.
- **`/checkv3`.** Its multipart form streams the message and the SMTP details
  together, and its answer is read as rspamd sends it (multipart, verdict in
  the `result` part). The controller serves it since 4.1.1, hence the minimum
  version; a 404 means too old. Training stays on `/learnspam` and
  `/learnham`, as rspamd has no newer learning endpoint.
- **Rules first and last, filters replaced.** The account's configured rules
  (rulesets, DMARC policy, subjectpass, Introbox) are respected; mox's own
  filters are replaced by rspamd, so there is never a double verdict.
- **mox's reputation stays replaced.** It is a filter, not a rule. A
  conclusive domain reputation in mox (most recent mail from a domain mostly
  not junk) let any sender at a large free-mail domain skip all content
  checks; with rspamd, only the exact address the account has written to is
  trusted.
- **Known correspondents need DMARC.** Accepting mail without rspamd because
  the account has written to the From address requires that From address to
  be DMARC-validated; mox's own check has no such condition. mox's other
  exact-address reputation (earlier mail from the address) is not used: it
  learns from the junk flags rspamd itself sets, so one harmless message from
  a spammer would let later ones bypass rspamd.
- **DMARC reject before rspamd,** to respect the domain owner's published
  policy (RFC 7489), with mox's exceptions for forwarded mail.
- **No ARC.** ARC is being moved to Historic by the IETF, and upstream mox
  declined an implementation. Forwarded mail stays handled by rulesets with
  `IsForward`, and on the rspamd side by `external_relay`.
- **Junk to the Junk mailbox, not rejected.** A delivered junk message can be
  rescued and trained; a rejected one only through Rejects.
- **Greylisting is never accepted into a mailbox.** The sender must retry, or
  greylisting would not work.
- **One check per recipient,** so per-user statistics are right for each
  recipient, bounded by the 5-minute limit.
- **Training in the background after commit,** best effort, with an in-memory
  queue: losing a few training messages on restart is acceptable; slowing down
  IMAP is not.
- **Security.** Redirects are refused (Go would forward the password header to
  the redirect target). The URL may appear in errors, so it may not hold
  credentials, a query or a fragment; the passwords have their own fields and
  are never logged. Passwords with control characters are refused, as they
  cannot be sent in a header.
- **Identification.** Requests carry a `User-Agent` naming muxx and its
  version, with `(rspamd)`, as mox's other HTTP clients do.

## What testing changed

- **Ruleset mailbox over the junk verdict: dropped.** An early design let a
  matching ruleset's mailbox win over rspamd's junk verdict. On a live system
  this broke a common setup: a ruleset for forwarded mail that delivers to
  Inbox. mox applies the account's automatic junk flags to every added
  message, Inbox is neutral, so `$Junk` was cleared and junk landed in Inbox
  looking like normal mail. For a ruleset mailbox that is "not junk", it would
  even set `$NotJunk` and train the spam as ham. Junk verdicts now always go to
  Junk.
- **Training rule.** Training first followed rspamd's usual Sieve rule (moves
  into and out of Junk). It was changed to follow the account's junk filter,
  so muxx and rspamd always agree and training survives removing rspamd. That
  change lost one case, Junk → Inbox, which review caught; the rescue rule
  restores it.
- **Both flags set.** A client adding `$NotJunk` while `$Junk` is still set
  first trained rspamd as ham while the junk filter treated the message as
  untrained. Both now follow the junk filter: nothing is learned until only
  one flag remains.
- **`EnablePassword`.** Training failed with 401 against an rspamd with both
  passwords set: rspamd only accepts training with `enable_password`. Measured
  as well: `enable_password` is also accepted for read-only commands.
- **GTUBE.** rspamd answers the GTUBE test pattern as "skipped" with a
  `reject` action. "Skipped" alone is therefore not "no verdict".
- **Training messages.** Go's HTTP client closes a request body; mox's message
  reader logged an error when closed twice. The body is passed wrapped so only
  muxx closes it.

## Not included

- **Marking the original when replying through SMTP.** The webmail sets
  `$NotJunk` on the message replied to; a reply sent from an IMAP client
  through SMTP does not. That would change mox's behaviour without rspamd too,
  so it is a separate feature.
- **`AcceptRejectsToMailbox` for greylisting and soft rejects:** for later
  discussion. Today the sender always retries.
- **Bulk training** of existing mailboxes into rspamd: not provided. rspamd's
  `rspamc learn_spam`/`learn_ham` can do it.
- **Checking outgoing mail** with rspamd, and therefore its `replies` module.
- **A per-account switch:** the `Rspamd` section is global.

## How it was built and tested

- Every behaviour has a test, and every test was checked to fail with the code
  it covers removed or reverted, so it cannot pass by accident. Tests run
  against a fake rspamd that answers like the real one.
- Before each commit: build, `go vet` (also for 386), `gofmt`, `ineffassign`,
  `staticcheck`, `govulncheck`, the full test suite and the race detector.
- Live tests against real rspamd (4.1.0 for "too old", 4.3.0, with and without
  Redis): every action, GTUBE, greylisting and the retry, oversized messages,
  rspamd down, Junk mailbox creation and role restore, per-user keys, training
  with each result, the admin line, and moves in a real mail client training
  both muxx and rspamd.
- Each design question was answered from mox's code, rspamd's documentation or
  source, and the relevant RFCs, and recorded with its reason.

## Code map

| Where | What |
|---|---|
| `rspamd/rspamd.go` | HTTP client: checks, training, status (`/stat`) |
| `rspamd/learn.go` | Background training queue |
| `rspamd/metrics.go` | Metrics |
| `smtpserver/rspamd.go` | Delivery decision with rspamd, Junk mailbox |
| `smtpserver/analyze.go` | Where rspamd is consulted |
| `store/rspamd.go` | Training rule, rescue rule, per-user address |
| `imapserver/server.go`, `webops/xops.go`, `webmail/api.go` | Training hooks for IMAP, web operations and webmail replies |
| `webadmin/admin.go`, `webadmin/admin.ts` | Admin page status line |
| `config/config.go`, `mox-/config.go` | `Rspamd` section and its validation |
