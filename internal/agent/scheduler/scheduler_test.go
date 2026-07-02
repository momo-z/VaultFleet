package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchedulerAddJobRunsEverySchedule(t *testing.T) {
	s := New()
	require.NoError(t, s.Start())
	defer s.Stop()

	var runs atomic.Int32
	require.NoError(t, s.AddJob("agent-1", "@every 1s", func() {
		runs.Add(1)
	}))

	assert.Eventually(t, func() bool {
		return runs.Load() > 0
	}, 1500*time.Millisecond, 25*time.Millisecond)
}

func TestSchedulerHasJobReflectsRegistration(t *testing.T) {
	s := New()

	assert.False(t, s.HasJob("agent-1"), "no job registered yet")

	require.NoError(t, s.AddJob("agent-1", "@every 1h", func() {}))
	assert.True(t, s.HasJob("agent-1"), "job should exist after AddJob")

	s.RemoveJob("agent-1")
	assert.False(t, s.HasJob("agent-1"), "job should be gone after RemoveJob")
}

func TestSchedulerRemoveJobStopsFutureRuns(t *testing.T) {
	s := New()
	require.NoError(t, s.Start())
	defer s.Stop()

	var runs atomic.Int32
	require.NoError(t, s.AddJob("agent-1", "@every 1s", func() {
		runs.Add(1)
	}))
	assert.Eventually(t, func() bool {
		return runs.Load() > 0
	}, 1500*time.Millisecond, 25*time.Millisecond)

	s.RemoveJob("agent-1")
	afterRemove := runs.Load()
	time.Sleep(1200 * time.Millisecond)

	assert.Equal(t, afterRemove, runs.Load())
}

func TestSchedulerUpdateScheduleReplacesExistingJob(t *testing.T) {
	s := New()
	require.NoError(t, s.Start())
	defer s.Stop()

	var oldRuns atomic.Int32
	var newRuns atomic.Int32
	require.NoError(t, s.AddJob("agent-1", "@every 1s", func() {
		oldRuns.Add(1)
	}))
	assert.Eventually(t, func() bool {
		return oldRuns.Load() > 0
	}, 1500*time.Millisecond, 25*time.Millisecond)

	require.NoError(t, s.UpdateSchedule("agent-1", "@every 1s", func() {
		newRuns.Add(1)
	}))
	oldAfterUpdate := oldRuns.Load()

	assert.Eventually(t, func() bool {
		return newRuns.Load() > 0
	}, 1500*time.Millisecond, 25*time.Millisecond)
	time.Sleep(1200 * time.Millisecond)

	assert.Equal(t, oldAfterUpdate, oldRuns.Load())
}

func TestSchedulerAcceptsFiveFieldCronByPrependingSeconds(t *testing.T) {
	s := New()

	require.NoError(t, s.AddJob("agent-1", "*/1 * * * *", func() {}))
	assert.Len(t, s.cron.Entries(), 1)
	assert.Equal(t, "0 */1 * * * *", normalizeCron("*/1 * * * *"))
}

func TestSchedulerRejectsInvalidCron(t *testing.T) {
	s := New()

	err := s.AddJob("agent-1", "not a cron", func() {})

	require.Error(t, err)
}

func TestSchedulerValidateRejectsInvalidCronWithoutAddingJob(t *testing.T) {
	s := New()

	require.NoError(t, s.Validate("*/1 * * * *"))
	require.Error(t, s.Validate("not a cron"))
	assert.Empty(t, s.cron.Entries())
}

func TestSchedulerInvalidUpdateKeepsExistingJob(t *testing.T) {
	s := New()

	require.NoError(t, s.AddJob("agent-1", "@every 1s", func() {}))
	entriesBefore := s.cron.Entries()
	require.Len(t, entriesBefore, 1)

	err := s.UpdateSchedule("agent-1", "not a cron", func() {})

	require.Error(t, err)
	entriesAfter := s.cron.Entries()
	require.Len(t, entriesAfter, 1)
	assert.Equal(t, entriesBefore[0].ID, entriesAfter[0].ID)
	assert.Equal(t, entriesBefore[0].Schedule, entriesAfter[0].Schedule)
}

func TestSchedulerConcurrentUpdateAndRemoveDoesNotLeaveTrackedDuplicates(t *testing.T) {
	s := New()
	require.NoError(t, s.Start())
	defer s.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.UpdateSchedule("agent-1", "@every 1s", func() {})
		}()
		go func() {
			defer wg.Done()
			s.RemoveJob("agent-1")
		}()
	}
	wg.Wait()

	s.mu.Lock()
	trackedID, tracked := s.entryID["agent-1"]
	require.LessOrEqual(t, len(s.entryID), 1)
	s.mu.Unlock()
	entries := s.cron.Entries()
	if !tracked {
		assert.Empty(t, entries)
		return
	}
	require.Len(t, entries, 1)
	assert.Equal(t, trackedID, entries[0].ID)
}
