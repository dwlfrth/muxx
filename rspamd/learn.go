package rspamd

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/metrics"
	"github.com/mjl-/mox/mlog"
)

// LearnQueueSize is the maximum number of messages waiting to be sent for
// training. More are dropped.
const LearnQueueSize = 1000

// LearnJob is a message to train rspamd with.
type LearnJob struct {
	Log       mlog.Log
	Conf      config.Rspamd
	Spam      bool
	DeliverTo string // User for per-user statistics, empty for none.

	// Open returns the message. Called when the message is sent. An error matching
	// fs.ErrNotExist means the message was removed in the meantime.
	Open func() (io.ReadCloser, error)
}

// Results of training, as counted in metrics.
const (
	learnOK      = "ok"
	learnAlready = "already"
	learnRefused = "refused"
	learnFailed  = "failed"
	learnDropped = "dropped"
)

var (
	learnQueue     = make(chan LearnJob, LearnQueueSize)
	learnStartOnce sync.Once
)

// QueueLearn queues a message for training. Messages are sent in the background,
// one at a time, so a slow or unavailable rspamd does not slow down the caller.
// Training is best-effort: a message is dropped when the queue is full, and
// queued messages are lost when mox stops.
func QueueLearn(job LearnJob) {
	learnStartOnce.Do(func() {
		go learnWorker()
	})
	select {
	case learnQueue <- job:
	default:
		job.Log.Error("rspamd training queue full, dropping message", slog.Bool("spam", job.Spam))
		metricLearn.WithLabelValues(learnDropped).Inc()
	}
}

func learnWorker() {
	for job := range learnQueue {
		learnOne(job)
	}
}

func learnOne(job LearnJob) {
	log := job.Log
	defer func() {
		if x := recover(); x != nil {
			log.Error("unhandled panic while training rspamd", slog.Any("x", x))
			debug.PrintStack()
			metrics.PanicInc(metrics.Store)
			metricLearn.WithLabelValues(learnFailed).Inc()
		}
	}()

	msg, err := job.Open()
	if errors.Is(err, fs.ErrNotExist) {
		// Removed by the user before we got to it.
		log.Debug("message for training rspamd no longer exists", slog.Bool("spam", job.Spam))
		metricLearn.WithLabelValues(learnDropped).Inc()
		return
	} else if err != nil {
		log.Errorx("opening message for training rspamd", err, slog.Bool("spam", job.Spam))
		metricLearn.WithLabelValues(learnFailed).Inc()
		return
	}
	defer func() {
		err := msg.Close()
		log.Check(err, "closing message after training rspamd")
	}()

	// The HTTP client closes a request body that has a Close method, and we close msg
	// ourselves above, so hide its Close.
	err = Learn(context.Background(), job.Conf, job.Spam, job.DeliverTo, io.NopCloser(msg))
	result := learnOK
	switch {
	case errors.Is(err, ErrAlreadyLearned):
		result = learnAlready
	case errors.Is(err, ErrRefused):
		result = learnRefused
	case err != nil:
		result = learnFailed
	}
	metricLearn.WithLabelValues(result).Inc()
	if result == learnFailed {
		log.Errorx("training rspamd", err, slog.Bool("spam", job.Spam))
	} else {
		log.Debugx("trained rspamd", err, slog.Bool("spam", job.Spam), slog.String("result", result))
	}
}

// LearnQueueLength returns the number of messages waiting to be sent for training.
func LearnQueueLength() int {
	return len(learnQueue)
}
