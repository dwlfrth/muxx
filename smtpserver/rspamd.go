package smtpserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/dmarc"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/rspamd"
	"github.com/mjl-/mox/smtp"
	"github.com/mjl-/mox/store"
	"github.com/mjl-/mox/subjectpass"
)

// rspamdCheckFunc checks the message with rspamd for one delivered address. If
// reused is set, the result is that of an earlier delivered address of the
// message, because the time limit for checks was reached.
type rspamdCheckFunc func(smtpRcptTo, deliverTo smtp.Path) (result rspamd.Result, reused bool, err error)

// rspamdChecksLimit is the time after which no more checks are started for a
// message: the remaining delivered addresses get the most recent result. Half of
// the 10 minutes the sender waits for our reply (RFC 5321 section 4.5.3.2.6),
// leaving time for a check still running and for the rest of the delivery.
var rspamdChecksLimit = 5 * time.Minute

const (
	reasonRspamdAccept     = "rspamd-accept"
	reasonRspamdJunk       = "rspamd-junk"
	reasonRspamdReject     = "rspamd-reject"
	reasonRspamdTempReject = "rspamd-tempreject"
)

// rspamdCheck returns a function to check the message for a delivered address, or
// nil if rspamd is not configured. The message is checked with our Received header
// for the recipient in front, as we store it: the receiving server must add it
// (RFC 5321 section 4.4).
//
// Recipients are checked one after another, each with its own per-user
// statistics. So the sender gets our reply within the 10 minutes it waits (RFC
// 5321 section 4.5.3.2.6): once a check failed, the error is returned for the
// remaining recipients without asking rspamd again; and once checks took
// rspamdChecksLimit, the most recent result is returned for the remaining
// recipients. A check still running then is not stopped, it ends at Timeout.
func (c *conn) rspamdCheck(ctx context.Context, recvHdrFor func(string) string, iprevName string, dataFile *os.File, size int64) rspamdCheckFunc {
	conf := mox.Conf.Static.Rspamd
	if conf == nil {
		return nil
	}
	start := time.Now()
	var failed error
	var last *rspamd.Result
	return func(smtpRcptTo, deliverTo smtp.Path) (rspamd.Result, bool, error) {
		if failed != nil {
			return rspamd.Result{}, false, failed
		} else if last != nil && time.Since(start) >= rspamdChecksLimit {
			return *last, true, nil
		}
		req := rspamd.Request{
			IP:        c.remoteIP,
			Helo:      c.hello.Domain.Name(),
			Hostname:  iprevName,
			Rcpts:     []string{smtpRcptTo.String()},
			QueueID:   mox.ReceivedID(c.cid),
			DeliverTo: store.RspamdUser(smtp.NewAddress(deliverTo.Localpart, deliverTo.IPDomain.Domain)),
		}
		if len(c.hello.IP) > 0 {
			req.Helo = smtp.AddressLiteral(c.hello.IP)
		}
		if !c.mailFrom.IsZero() {
			req.MailFrom = c.mailFrom.String()
		}
		msg := io.MultiReader(strings.NewReader(recvHdrFor(smtpRcptTo.String())), io.NewSectionReader(dataFile, 0, size))
		result, err := rspamd.Check(ctx, *conf, req, msg)
		if err != nil {
			failed = err
			return rspamd.Result{}, false, err
		}
		last = &result
		return result, false, nil
	}
}

// rspamdAnalyze decides about delivery with rspamd: a message whose
// DMARC-validated From address the account has sent to in the past 3 years, or
// with a valid subjectpass token, is accepted, other messages are judged by
// rspamd. The account's rules apply around rspamd's verdict: the Introbox and the
// subjectpass hint. If rspamd cannot be used, false is returned and the caller
// continues with mox's own checks.
func rspamdAnalyze(ctx context.Context, log mlog.Log, d delivery, mailbox string, addReasonText func(format string, args ...any), reject func(code int, secode string, errmsg string, err error, reason string) analysis) (analysis, bool) {
	accept := func(mailbox, mailboxDestined, reason string) (analysis, bool) {
		return analysis{d: d, accept: true, mailbox: mailbox, mailboxDestined: mailboxDestined, reason: reason}, true
	}
	conf, _ := d.acc.Conf()

	// As for mox's own checks, mail from senders without reputation goes to the
	// Introbox, if configured. Reputation is only used to choose the mailbox here.
	acceptIntrobox := func(reason string) (analysis, bool) {
		if mailbox != "Inbox" || conf.Introbox == "" || d.m.IsForward {
			return accept(mailbox, "", reason)
		}
		method, err := rspamdReputation(ctx, log, d, mailbox)
		if err != nil {
			log.Infox("determining reputation", err, slog.Any("message", d.m))
			addReasonText("determining reputation: %v", err)
			return reject(smtp.C451LocalErr, smtp.SeSys3Other0, "error processing", err, reasonReputationError), true
		} else if method != methodMsgfromFull && method != methodMsgtoFull {
			log.Info("delivering message without established reputation to introbox", slog.String("mailbox", conf.Introbox))
			return accept(conf.Introbox, mailbox, reason)
		}
		return accept(mailbox, "", reason)
	}

	if d.m.MsgFromValidated && d.m.MsgFromDomain != "" {
		sent, err := rspamdSentTo(ctx, d)
		if err != nil {
			log.Errorx("looking up earlier recipients", err)
			addReasonText("looking up earlier recipients: %v", err)
			return reject(smtp.C451LocalErr, smtp.SeSys3Other0, "error processing", err, reasonReputationError), true
		} else if sent {
			addReasonText("validated message-from address was earlier message recipient, accepted without rspamd")
			return accept(mailbox, "", string(methodMsgtoFull))
		}
	}

	// A valid subjectpass token, given in an earlier refusal, is accepted as by mox's
	// own checks.
	var subjectpassKey string
	if conf.SubjectPass.Period > 0 {
		var err error
		subjectpassKey, err = d.acc.Subjectpass(d.canonicalAddress)
		if err != nil {
			log.Errorx("get key for verifying subject token", err)
			addReasonText("subject pass error: %v", err)
			return reject(smtp.C451LocalErr, smtp.SeSys3Other0, "error processing", err, reasonSubjectpassError), true
		}
		err = subjectpass.Verify(log.Logger, d.dataFile, []byte(subjectpassKey), conf.SubjectPass.Period)
		log.Infox("pass by subject token", err, slog.Bool("pass", err == nil))
		if err == nil {
			addReasonText("message has valid subjectpass token in subject, accepted without rspamd")
			return acceptIntrobox(reasonSubjectpass)
		}
	}

	result, reused, err := d.rspamdCheck(d.smtpRcptTo, d.deliverTo)
	// The reason header is stored with the message for the user, so for errors talking
	// to rspamd, which can include its internal URL, it only says rspamd was
	// unavailable. The log has the details.
	reasonErr := "unavailable"
	if errors.Is(err, rspamd.ErrTooOld) {
		reasonErr = err.Error()
	}
	if err == nil && result.IsSkipped && (result.Action == "" || result.Action == rspamd.ActionNoAction) {
		// A skip with a verdict, e.g. for the GTUBE test pattern, is applied below.
		err = errors.New("rspamd skipped the message without verdict")
		reasonErr = err.Error()
	} else if err == nil && !result.Action.Known() {
		err = fmt.Errorf("unknown rspamd action %q", result.Action)
		reasonErr = err.Error()
	}
	if err != nil {
		rspamd.CheckResult(rspamd.ResultFallback)
		log.Errorx("rspamd check failed, mox's own checks decide", err)
		addReasonText("rspamd: %s, mox's own checks decide", reasonErr)
		return analysis{}, false
	}
	// The reason header does not name the earlier recipient: it may be a Bcc.
	var reusedText string
	if reused {
		rspamd.CheckResult(rspamd.ResultReused)
		reusedText = "result of an earlier recipient of this message reused, time limit for rspamd checks reached: "
	} else {
		rspamd.CheckResult(string(result.Action))
	}
	log.Info("rspamd analyzed",
		slog.String("action", string(result.Action)),
		slog.Float64("score", result.Score),
		slog.Float64("requiredscore", result.RequiredScore),
		slog.Bool("reused", reused))
	addReasonText("rspamd: %saction %s, score %.2f, required %.2f, symbols: %s", reusedText, result.Action, result.Score, result.RequiredScore, rspamdSymbols(result.Symbols))

	switch result.Action {
	case rspamd.ActionNoAction:
		return acceptIntrobox(reasonRspamdAccept)

	case rspamd.ActionAddHeader, rspamd.ActionRewriteSubject:
		// Delivered as junk, to the Junk mailbox, created if needed, without changing the
		// message, also for a matching ruleset: in its mailbox, the account's automatic
		// junk flags could clear $Junk, or even set $NotJunk and train the junk filter
		// with it as nonjunk. With NoJunkMailboxCreate and no Junk mailbox, to the normal
		// mailbox, where the automatic junk flags may change $Junk, as for any delivery.
		d.m.Junk = true
		d.m.Notjunk = false
		junkMailbox, err := rspamdJunkMailbox(ctx, d)
		if err != nil {
			log.Errorx("finding junk mailbox", err)
			addReasonText("finding junk mailbox: %v", err)
			return reject(smtp.C451LocalErr, smtp.SeSys3Other0, "error processing", err, reasonRspamdJunk), true
		} else if junkMailbox == "" || junkMailbox == mailbox {
			return accept(mailbox, "", reasonRspamdJunk)
		}
		return accept(junkMailbox, mailbox, reasonRspamdJunk)

	case rspamd.ActionReject:
		// As mox's own checks, a first-time sender with DMARC pass is told how to get a
		// message through with a subjectpass token.
		if subjectpassKey != "" && d.dmarcResult.Status == dmarc.StatusPass {
			method, err := rspamdReputation(ctx, log, d, mailbox)
			if err != nil {
				log.Infox("determining reputation for subjectpass hint", err)
			} else if method == methodNone {
				log.Info("permanent reject with subjectpass hint of email without reputation")
				pass := subjectpass.Generate(log.Logger, d.msgFrom, []byte(subjectpassKey), time.Now())
				addReasonText("reject with request to try again with subjectpass token in subject")
				return reject(smtp.C550MailboxUnavail, smtp.SePol7DeliveryUnauth1, subjectpass.Explanation+pass, nil, reasonGiveSubjectpass), true
			}
		}
		return reject(smtp.C550MailboxUnavail, smtp.SePol7DeliveryUnauth1, "rejected as spam", nil, reasonRspamdReject), true

	case rspamd.ActionGreylist, rspamd.ActionSoftReject:
		// Not through reject: a ruleset's AcceptRejectsToMailbox must not accept the
		// message, the sender has to try again. A copy is kept in the Rejects mailbox,
		// and removed when the message is delivered later.
		// Reply 450 and 4.7.0, for greylisting (RFC 6647 section 5) and other policy
		// reasons. 4.7.1 is only defined as permanent (RFC 3463 section 3.8).
		return analysis{
			d:               d,
			mailbox:         conf.RejectsMailbox,
			mailboxDestined: mailbox,
			code:            smtp.C450MailboxUnavail,
			secode:          smtp.SePol7Other0,
			userError:       true,
			errmsg:          "try again later",
			reason:          reasonRspamdTempReject,
		}, true
	}
	// Not reached: unknown actions fall back to mox's checks above.
	return analysis{}, false
}

// rspamdSentTo returns whether the account sent a message to the From address of
// the message in the past 3 years, as for mox's reputation method "msgtofull".
func rspamdSentTo(ctx context.Context, d delivery) (sent bool, rerr error) {
	d.acc.WithRLock(func() {
		rerr = d.acc.DB.Read(ctx, func(tx *bstore.Tx) error {
			q := bstore.QueryTx[store.Recipient](tx)
			q.FilterEqual("Localpart", d.m.MsgFromLocalpart)
			q.FilterEqual("Domain", d.m.MsgFromDomain)
			q.FilterGreaterEqual("Sent", time.Now().Add(-3*365*24*time.Hour))
			var err error
			sent, err = q.Exists()
			return err
		})
	})
	return
}

// rspamdReputation returns the method by which mox's reputation knows the sender,
// for the Introbox and the subjectpass hint.
func rspamdReputation(ctx context.Context, log mlog.Log, d delivery, mailbox string) (method reputationMethod, rerr error) {
	d.acc.WithRLock(func() {
		rerr = d.acc.DB.Read(ctx, func(tx *bstore.Tx) error {
			var mailboxID int64 = -1
			mb, err := d.acc.MailboxFind(tx, mailbox)
			if err != nil {
				return fmt.Errorf("finding destination mailbox: %w", err)
			}
			if mb != nil {
				mailboxID = mb.ID
			}
			_, _, method, _, err = reputation(tx, log, d.m, mailboxID, d.smtputf8)
			return err
		})
	})
	return
}

// rspamdJunkMailbox returns the name of the mailbox with the Junk special-use
// role. If there is none, it is created with the name configured for Junk in
// InitialMailboxes, or "Junk", or an existing mailbox with that name gets the role.
// Empty if there is none and NoJunkMailboxCreate is set.
func rspamdJunkMailbox(ctx context.Context, d delivery) (name string, rerr error) {
	junkMailbox := func(tx *bstore.Tx) (store.Mailbox, error) {
		return bstore.QueryTx[store.Mailbox](tx).FilterEqual("Expunged", false).FilterEqual("Junk", true).Get()
	}
	d.acc.WithRLock(func() {
		rerr = d.acc.DB.Read(ctx, func(tx *bstore.Tx) error {
			mb, err := junkMailbox(tx)
			if err == nil {
				name = mb.Name
			} else if err != bstore.ErrAbsent {
				return err
			}
			return nil
		})
	})
	if rerr != nil || name != "" || mox.Conf.Static.Rspamd.NoJunkMailboxCreate {
		return
	}

	jname := mox.Conf.Static.InitialMailboxes.SpecialUse.Junk
	if jname == "" {
		jname = "Junk"
	}
	var changes []store.Change
	d.acc.WithWLock(func() {
		rerr = d.acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			// Check again, it may have been created in the meantime.
			if mb, err := junkMailbox(tx); err == nil {
				name = mb.Name
				return nil
			} else if err != bstore.ErrAbsent {
				return err
			}
			var modseq store.ModSeq
			mb, chl, err := d.acc.MailboxEnsure(tx, jname, true, store.SpecialUse{Junk: true}, &modseq)
			if err != nil {
				return fmt.Errorf("creating junk mailbox: %v", err)
			}
			changes = append(changes, chl...)
			if !mb.Junk {
				// An existing mailbox with the name, without the role.
				if modseq == 0 {
					if modseq, err = d.acc.NextModSeq(tx); err != nil {
						return err
					}
				}
				mb.Junk = true
				mb.ModSeq = modseq
				if err := tx.Update(&mb); err != nil {
					return fmt.Errorf("giving mailbox junk role: %v", err)
				}
				changes = append(changes, mb.ChangeSpecialUse())
			}
			name = mb.Name
			return nil
		})
		if rerr == nil && len(changes) > 0 {
			store.BroadcastChanges(d.acc, changes)
		}
	})
	return
}

// rspamdSymbols returns the symbols that contributed to the score, largest
// contribution first, for the reason header.
func rspamdSymbols(symbols map[string]rspamd.Symbol) string {
	var l []rspamd.Symbol
	for _, s := range symbols {
		if s.Score != 0 {
			l = append(l, s)
		}
	}
	slices.SortFunc(l, func(a, b rspamd.Symbol) int {
		if c := cmp.Compare(math.Abs(b.Score), math.Abs(a.Score)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	const max = 10
	var more int
	if len(l) > max {
		l, more = l[:max], len(l)-max
	}
	var b strings.Builder
	for i, s := range l {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s(%.2f)", s.Name, s.Score)
	}
	if more > 0 {
		fmt.Fprintf(&b, " and %d more", more)
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}
