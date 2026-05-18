package events

import "sync"

type EventType string

const (
	PolicyChanged EventType = "policy_changed"
	AgentOnline   EventType = "agent_online"
	AgentOffline  EventType = "agent_offline"
)

type Event struct {
	Type    EventType
	Payload interface{}
}

type Handler func(Event)

type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
}

func NewBus() *Bus {
	return &Bus{
		handlers: make(map[EventType][]Handler),
	}
}

func (b *Bus) Subscribe(eventType EventType, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

func (b *Bus) Publish(event Event) {
	b.mu.RLock()
	handlers := append([]Handler(nil), b.handlers[event.Type]...)
	b.mu.RUnlock()

	for _, handler := range handlers {
		handler(event)
	}
}
