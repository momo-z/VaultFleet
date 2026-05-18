package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBus_PublishSubscribeReceivesTwoEvents(t *testing.T) {
	bus := NewBus()
	var received []Event

	bus.Subscribe(PolicyChanged, func(event Event) {
		received = append(received, event)
	})

	first := Event{Type: PolicyChanged, Payload: "first"}
	second := Event{Type: PolicyChanged, Payload: "second"}
	bus.Publish(first)
	bus.Publish(second)

	assert.Equal(t, []Event{first, second}, received)
}

func TestBus_NoSubscribersDoesNotPanic(t *testing.T) {
	bus := NewBus()

	assert.NotPanics(t, func() {
		bus.Publish(Event{Type: AgentOnline, Payload: "agent-1"})
	})
}

func TestBus_MultipleSubscribersAllInvoked(t *testing.T) {
	bus := NewBus()
	calls := []string{}

	bus.Subscribe(AgentOffline, func(Event) {
		calls = append(calls, "first")
	})
	bus.Subscribe(AgentOffline, func(Event) {
		calls = append(calls, "second")
	})

	bus.Publish(Event{Type: AgentOffline, Payload: "agent-1"})

	assert.ElementsMatch(t, []string{"first", "second"}, calls)
}
