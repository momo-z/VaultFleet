package scheduler

import (
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
