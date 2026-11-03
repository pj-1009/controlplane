package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allTaskStatuses = []TaskStatus{TaskAccepted, TaskInProgress, TaskDone, TaskFailed}

var allTenantStatuses = []TenantStatus{
	TenantProvisioning, TenantActive, TenantUpdating, TenantDestroying, TenantDestroyed, TenantFailed,
}

func TestCanAdvanceTask(t *testing.T) {
	allowed := map[[2]TaskStatus]bool{
		{TaskAccepted, TaskInProgress}: true,
		{TaskAccepted, TaskDone}:       true, // forward skip
		{TaskAccepted, TaskFailed}:     true, // forward skip
		{TaskInProgress, TaskDone}:     true,
		{TaskInProgress, TaskFailed}:   true,
	}

	// Every pair not listed above must be rejected.
	for _, from := range allTaskStatuses {
		for _, to := range allTaskStatuses {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				assert.Equal(t, allowed[[2]TaskStatus{from, to}], CanAdvanceTask(from, to))
			})
		}
	}

	t.Run("unknown statuses", func(t *testing.T) {
		assert.False(t, CanAdvanceTask("bogus", TaskDone))
		assert.False(t, CanAdvanceTask(TaskAccepted, "bogus"))
	})
}

func TestTaskStatusTerminal(t *testing.T) {
	tests := []struct {
		status   TaskStatus
		terminal bool
	}{
		{TaskAccepted, false},
		{TaskInProgress, false},
		{TaskDone, true},
		{TaskFailed, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			assert.Equal(t, tt.terminal, tt.status.IsTerminal())
		})
	}
}

func TestStatusesBelow(t *testing.T) {
	assert.Empty(t, StatusesBelow(TaskAccepted))
	assert.Equal(t, []TaskStatus{TaskAccepted}, StatusesBelow(TaskInProgress))
	assert.Equal(t, []TaskStatus{TaskAccepted, TaskInProgress}, StatusesBelow(TaskDone))
	assert.Equal(t, []TaskStatus{TaskAccepted, TaskInProgress}, StatusesBelow(TaskFailed))
}

func TestCanUpdateAndDeleteTenant(t *testing.T) {
	tests := []struct {
		status    TenantStatus
		canUpdate bool
		canDelete bool
	}{
		{TenantProvisioning, false, false},
		{TenantActive, true, true},
		{TenantUpdating, false, false},
		{TenantDestroying, false, false},
		{TenantDestroyed, false, false},
		{TenantFailed, false, true},
	}
	require.Len(t, tests, len(allTenantStatuses))
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			assert.Equal(t, tt.canUpdate, CanUpdateTenant(tt.status), "update")
			assert.Equal(t, tt.canDelete, CanDeleteTenant(tt.status), "delete")
		})
	}
}

func TestPendingStatus(t *testing.T) {
	tests := map[TaskType]TenantStatus{
		TaskDeploy:  TenantProvisioning,
		TaskUpdate:  TenantUpdating,
		TaskDestroy: TenantDestroying,
	}
	for typ, want := range tests {
		got, err := PendingStatus(typ)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := PendingStatus("bogus")
	require.Error(t, err)
}

func TestTenantStatusAfterTask(t *testing.T) {
	type key struct {
		current TenantStatus
		typ     TaskType
		result  TaskStatus
	}
	allowed := map[key]TenantStatus{
		{TenantProvisioning, TaskDeploy, TaskDone}:   TenantActive,
		{TenantProvisioning, TaskDeploy, TaskFailed}: TenantFailed,
		{TenantUpdating, TaskUpdate, TaskDone}:       TenantActive,
		{TenantUpdating, TaskUpdate, TaskFailed}:     TenantFailed,
		{TenantDestroying, TaskDestroy, TaskDone}:    TenantDestroyed,
		{TenantDestroying, TaskDestroy, TaskFailed}:  TenantFailed,
	}

	// Exhaustively check every combination; anything not listed is an error.
	for _, current := range allTenantStatuses {
		for _, typ := range []TaskType{TaskDeploy, TaskUpdate, TaskDestroy} {
			for _, result := range allTaskStatuses {
				k := key{current, typ, result}
				t.Run(string(current)+"/"+string(typ)+"/"+string(result), func(t *testing.T) {
					got, err := TenantStatusAfterTask(current, typ, result)
					want, ok := allowed[k]
					if !ok {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, want, got)
				})
			}
		}
	}
}
