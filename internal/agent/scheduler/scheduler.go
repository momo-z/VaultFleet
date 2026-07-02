package scheduler

import (
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron    *cron.Cron
	parser  cron.Parser
	mu      sync.Mutex
	entryID map[string]cron.EntryID
}

func New() *Scheduler {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	return &Scheduler{
		cron:    cron.New(cron.WithParser(parser)),
		parser:  parser,
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
	parsedSchedule, err := s.parse(schedule)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entryID[agentID]; ok {
		s.cron.Remove(existing)
	}
	entryID := s.cron.Schedule(parsedSchedule, cron.FuncJob(fn))
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

func (s *Scheduler) HasJob(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entryID[key]
	return ok
}

func (s *Scheduler) UpdateSchedule(agentID string, schedule string, fn func()) error {
	parsedSchedule, err := s.parse(schedule)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entryID[agentID]; ok {
		s.cron.Remove(existing)
	}
	entryID := s.cron.Schedule(parsedSchedule, cron.FuncJob(fn))
	s.entryID[agentID] = entryID
	return nil
}

func (s *Scheduler) Validate(schedule string) error {
	_, err := s.parse(schedule)
	return err
}

func (s *Scheduler) parse(schedule string) (cron.Schedule, error) {
	return s.parser.Parse(normalizeCron(schedule))
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
