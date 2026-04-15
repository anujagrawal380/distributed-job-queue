package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeasePriorityOrder(t *testing.T) {
	core, _ := setupTestCore(t)

	lowID, err := core.SubmitWithOptions([]byte("low"), SubmitOptions{MaxRetries: 0, Priority: 1})
	require.NoError(t, err)
	highID, err := core.SubmitWithOptions([]byte("high"), SubmitOptions{MaxRetries: 0, Priority: 10})
	require.NoError(t, err)
	midID, err := core.SubmitWithOptions([]byte("mid"), SubmitOptions{MaxRetries: 0, Priority: 5})
	require.NoError(t, err)

	j, err := core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, highID, j.ID, "highest priority should lease first")

	j, err = core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, midID, j.ID)

	j, err = core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, lowID, j.ID)
}

func TestLeaseFIFOWhenSamePriority(t *testing.T) {
	core, _ := setupTestCore(t)

	firstID, _ := core.Submit([]byte("first"), 0)
	time.Sleep(5 * time.Millisecond) // ensure distinct CreatedAt
	secondID, _ := core.Submit([]byte("second"), 0)

	j, err := core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, firstID, j.ID, "older job should lease first at same priority")

	j, err = core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, secondID, j.ID)
}

func TestDelayedJobNotLeasableYet(t *testing.T) {
	core, _ := setupTestCore(t)

	future := time.Now().Add(1 * time.Hour)
	_, err := core.SubmitWithOptions([]byte("later"), SubmitOptions{MaxRetries: 0, RunAt: future})
	require.NoError(t, err)

	_, err = core.Lease(30 * time.Second)
	assert.Error(t, err, "delayed job should not be leasable before RunAt")
}

func TestDelayedJobLeasableAfterRunAt(t *testing.T) {
	core, _ := setupTestCore(t)

	past := time.Now().Add(-1 * time.Second)
	id, err := core.SubmitWithOptions([]byte("now"), SubmitOptions{MaxRetries: 0, RunAt: past})
	require.NoError(t, err)

	j, err := core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, id, j.ID)
}

func TestImmediateJobBeatsDelayed(t *testing.T) {
	core, _ := setupTestCore(t)

	_, err := core.SubmitWithOptions([]byte("later"), SubmitOptions{
		MaxRetries: 0, Priority: 100, RunAt: time.Now().Add(1 * time.Hour),
	})
	require.NoError(t, err)
	nowID, err := core.SubmitWithOptions([]byte("now"), SubmitOptions{MaxRetries: 0, Priority: 0})
	require.NoError(t, err)

	j, err := core.Lease(30 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, nowID, j.ID, "runnable low-priority job should beat scheduled high-priority one")
}
