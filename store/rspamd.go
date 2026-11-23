package store

import (
	"bufio"
	"bytes"
	"io"
	"net/textproto"
	"os"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/rspamd"
	"github.com/mjl-/mox/smtp"
)

// RspamdLearn is a message to train rspamd with. They are collected while a
// change is made in a transaction, and queued with Account.RspamdQueue after the
// transaction is committed, so changes that are rolled back don't train rspamd.
type RspamdLearn struct {
	MsgID     int64
	Spam      bool
	DeliverTo string
}

// RspamdLearnFlags returns whether a change of the flags of a message, from
// oflags to nflags, trains rspamd. rspamd follows the account's junk filter: a
// message with only $Junk is spam, with only $NotJunk ham, otherwise it is not
// trained. The flags change explicitly by a mail client or user, or by a move or
// copy to a mailbox with automatic junk flags (for a copy, oflags are those of the
// original). rspamd learns when the message becomes spam or ham. It cannot forget
// a message, so a message that the junk filter untrains, by having neither or both
// flags or by being removed, keeps its training in rspamd. Messages are not
// learned when added, e.g. on delivery with rspamd's verdict. Always false without
// rspamd configured.
func RspamdLearnFlags(m Message, oflags, nflags Flags) (RspamdLearn, bool) {
	if mox.Conf.Static.Rspamd == nil {
		return RspamdLearn{}, false
	}
	oclass, oknown := flagsClass(oflags)
	nclass, nknown := flagsClass(nflags)
	if nknown && (!oknown || oclass != nclass) {
		return rspamdLearn(m, nclass), true
	}
	return RspamdLearn{}, false
}

// RspamdJunkFlagsForMailboxMove sets the junk flags for a move or copy, as
// Message.JunkFlagsForMailboxMove, and with rspamd configured, also marks a
// message moved or copied out of the mailbox with the Junk special-use role as
// nonjunk if its new mailbox is neutral, e.g. Inbox with the default automatic
// junk flags: taking a message out of Junk says it is not junk, for rspamd and the
// junk filter. Not for the mailbox with the Trash role: deleting is not rescuing.
func (m *Message) RspamdJunkFlagsForMailboxMove(mbSrc, mbDst Mailbox, conf config.Account) {
	m.JunkFlagsForMailboxMove(mbSrc, mbDst, conf)
	if mox.Conf.Static.Rspamd != nil && mbSrc.Junk && !mbDst.Junk && !mbDst.Trash && !m.Junk && !m.Notjunk {
		m.Notjunk = true
	}
}

// flagsClass returns whether flags make a message spam, if they make it spam or
// ham, as Message.needsTraining.
func flagsClass(f Flags) (spam, known bool) {
	return f.Junk, f.Junk != f.Notjunk
}

func rspamdLearn(m Message, spam bool) RspamdLearn {
	return RspamdLearn{m.ID, spam, rspamdDeliveredTo(m.MsgPrefix)}
}

// RspamdQueue queues messages for training rspamd, after the transaction that
// collected them was committed. The message file is sent, i.e. the message as
// received, without the headers we add at delivery, such as our Received header,
// which the check had in front.
func (a *Account) RspamdQueue(log mlog.Log, l []RspamdLearn) {
	conf := mox.Conf.Static.Rspamd
	if conf == nil {
		return
	}
	for _, x := range l {
		p := a.MessagePath(x.MsgID)
		rspamd.QueueLearn(rspamd.LearnJob{
			Log:       log,
			Conf:      *conf,
			Spam:      x.Spam,
			DeliverTo: x.DeliverTo,
			Open: func() (io.ReadCloser, error) {
				return os.Open(p)
			},
		})
	}
}

// RspamdUser returns the user for rspamd's per-user statistics for a delivered
// address: the address with its localpart canonical, i.e. without catchall
// separator suffix such as "+tag", as for the address lookup.
func RspamdUser(addr smtp.Address) string {
	if d, ok := mox.Conf.Domain(addr.Domain); ok {
		addr.Localpart = mox.CanonicalLocalpart(addr.Localpart, d)
	}
	return addr.String()
}

// rspamdDeliveredTo returns the user for per-user statistics from the
// Delivered-To header we add at delivery, as used for the check. Empty if absent,
// e.g. for imported messages, so rspamd falls back to its own choice.
func rspamdDeliveredTo(msgPrefix []byte) string {
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(msgPrefix, "\r\n"...)))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return ""
	}
	v := h.Get("Delivered-To")
	if v == "" {
		return ""
	}
	addr, err := smtp.ParseAddress(v)
	if err != nil {
		return ""
	}
	return RspamdUser(addr)
}
