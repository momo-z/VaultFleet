package scheduler

import (
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron    *cron.Cron
	mu      sync.Mutex
	entryID map[string]cron.EntryID
}

func New() *Scheduler {
	return &Scheduler{
		cron:    cron.New(cron.WithSeconds()),
		entryID: make(map[string]cron.EntryID),
	}
}

func (s *Scheduler) Start() error {
	s.cron.Start()
	return nil
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) AddJob(agentID string, schedule string, fn func()) error {
	normalized := normalizeCron(schedule)
	entryID, err := s.cron.AddFunc(normalized, fn)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entryID[agentID]; ok {
		s.cron.Remove(existing)
	}
	s.entryID[agentID] = entryID
	return nil
}

func (s *Scheduler) RemoveJob(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, ok := s.entryID[agentID]
	if !ok {
		return
	}
	s.cron.Remove(entryID)
	delete(s.entryID, agentID)
}

func (s *Scheduler) UpdateSchedule(agentID string, schedule string, fn func()) error {
	normalized := normalizeCron(schedule)
	entryID, err := s.cron.AddFunc(normalized, fn)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entryID[agentID]; ok {
		s.cron.Remove(existing)
	}
	s.entryID[agentID] = entryID
	return nil
}

func normalizeCron(schedule string) string {
	schedule = strings.TrimSpace(schedule)
	if strings.HasPrefix(schedule, "@") {
		return schedule
	}
	if len(strings.Fields(schedule)) == 5 {
		return "0 " + schedule
	}
	return schedule
}
