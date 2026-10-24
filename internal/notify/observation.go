package notify

import (
	"context"
	"time"

	"github.com/moutansos/op/internal/domain"
)

// Observation is local state evidence, not part of the forwarded notification protocol.
// ProjectID is the source's native ID; ProjectDirectory is used for local correlation.
type Observation struct {
	Source           Source
	SessionID        string
	ProjectID        string
	ProjectDirectory string
	Activity         domain.AgentActivity
	Detail           string
	Timestamp        time.Time
	Terminated       bool
	Notification     *Notification
	Coverage         string
}

const (
	CoverageNative           = "native"
	CoverageHooks            = "hooks"
	CoverageNotificationOnly = "notification-only"
)

type observationDelivery struct {
	callback    func(Observation)
	observation Observation
}

// SetObserver replaces the local observer. Callbacks run asynchronously, in enqueue
// order, without holding notifier locks or delaying provider delivery. Already queued
// callbacks may finish after replacement. Passing nil disables new observations.
func (n *Notifier) SetObserver(observer func(Observation)) {
	n.observerMu.Lock()
	n.observer = observer
	n.observerMu.Unlock()
}

// Observe implements the optional observation sink supported alongside Sender.
func (n *Notifier) Observe(observation Observation) {
	if observation.Timestamp.IsZero() {
		observation.Timestamp = time.Now()
	}
	if observation.Source == "" {
		observation.Source = SourceOpenCode
	}
	if observation.Notification != nil {
		copy := *observation.Notification
		copy.Choices = append([]Choice(nil), copy.Choices...)
		observation.Notification = &copy
	}
	n.observerMu.Lock()
	defer n.observerMu.Unlock()
	if n.observer == nil {
		return
	}
	// A slow observer cannot create an unbounded queue. At capacity coalesce
	// pending evidence for the same session, or evict the oldest observation.
	const maxPendingObservations = 1024
	if len(n.observations) >= maxPendingObservations {
		index := 0
		for i, delivery := range n.observations {
			if delivery.observation.Source == observation.Source && delivery.observation.SessionID == observation.SessionID {
				index = i
				break
			}
		}
		copy(n.observations[index:], n.observations[index+1:])
		n.observations = n.observations[:len(n.observations)-1]
	}
	n.observations = append(n.observations, observationDelivery{n.observer, observation})
	if !n.observing {
		n.observing = true
		go n.drainObservations()
	}
}

func (n *Notifier) drainObservations() {
	for {
		n.observerMu.Lock()
		if len(n.observations) == 0 {
			n.observing = false
			n.observerMu.Unlock()
			return
		}
		delivery := n.observations[0]
		n.observations[0] = observationDelivery{}
		n.observations = n.observations[1:]
		n.observerMu.Unlock()
		delivery.callback(delivery.observation)
	}
}

func observe(sender Sender, observation Observation) {
	if sink, ok := sender.(interface{ Observe(Observation) }); ok {
		sink.Observe(observation)
	}
}

func notificationObservation(notification Notification, coverage string) Observation {
	if notification.Source == "" {
		notification.Source = SourceOpenCode
	}
	if notification.Timestamp.IsZero() {
		notification.Timestamp = time.Now()
	}
	activity := domain.AgentActivityUnknown
	detail := ""
	switch notification.Type {
	case TypeIdle:
		activity = domain.AgentActivityIdle
	case TypeQuestion:
		activity, detail = domain.AgentActivityAwaitingInput, notification.Question
	case TypePermission:
		activity, detail = domain.AgentActivityPermissionRequired, notification.PermissionTitle
	}
	return Observation{Source: notification.Source, SessionID: notification.SessionID,
		ProjectID: notification.ProjectID, ProjectDirectory: notification.ProjectDirectory,
		Activity: activity, Detail: detail, Timestamp: notification.Timestamp,
		Notification: &notification, Coverage: coverage}
}

type observedContextKey struct{}

func alreadyObserved(ctx context.Context) context.Context {
	return context.WithValue(ctx, observedContextKey{}, true)
}

func sendNative(ctx context.Context, sender Sender, notification Notification, coverage string) error {
	observe(sender, notificationObservation(notification, coverage))
	return sender.Send(alreadyObserved(ctx), notification)
}
