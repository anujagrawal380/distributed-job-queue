package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatsReflectsJobStates(t *testing.T) {
	core, _ := setupTestCore(t)

	_, _ = core.Submit([]byte("a"), 0)
	bID, _ := core.Submit([]byte("b"), 0)
	_, _ = core.SubmitWithOptions([]byte("c"), SubmitOptions{
		MaxRetries: 0, RunAt: time.Now().Add(1 * time.Hour),
	})

	leased, err := core.Lease(30 * time.Second)
	require.NoError(t, err)
	require.NoError(t, core.Ack(leased.ID, []byte("ok"), ""))

	s := core.Stats()
	assert.Equal(t, 3, s.Total)
	// Both the remaining and the scheduled job are READY; Runnable/Scheduled
	// split them by whether RunAt has arrived.
	assert.Equal(t, 2, s.ByState[StateReady])
	assert.Equal(t, 1, s.ByState[StateAcked])
	assert.Equal(t, 1, s.Runnable, "one runnable (the remaining READY job)")
	assert.Equal(t, 1, s.Scheduled, "one waiting on RunAt")
	assert.EqualValues(t, 3, s.TotalSubmits)
	assert.EqualValues(t, 1, s.TotalAcks)
	_ = bID
}

func TestListJobsFilterAndPaginate(t *testing.T) {
	core, _ := setupTestCore(t)

	for i := 0; i < 5; i++ {
		_, _ = core.Submit([]byte("j"), 0)
		time.Sleep(2 * time.Millisecond) // distinct UpdatedAt
	}

	all := core.ListJobs(ListFilter{Limit: 10})
	assert.Len(t, all, 5)
	// newest first
	assert.True(t, all[0].UpdatedAt.After(all[1].UpdatedAt) || all[0].UpdatedAt.Equal(all[1].UpdatedAt))

	page := core.ListJobs(ListFilter{Limit: 2, Offset: 1})
	assert.Len(t, page, 2)

	onlyReady := core.ListJobs(ListFilter{State: StateReady, Limit: 100})
	assert.Len(t, onlyReady, 5)

	noAcked := core.ListJobs(ListFilter{State: StateAcked, Limit: 100})
	assert.Empty(t, noAcked)
}
