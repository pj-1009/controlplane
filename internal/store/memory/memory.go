// Package memory implements store.Store in process memory.
//
// It exists to prove that the rest of the code depends only on the
// store.Store contract: the same conformance suite and the same API,
// consumer, relay and end-to-end tests pass against it and against
// Postgres. It is also handy for local experiments. Data is lost when the
// process exits, and two processes don't share it, so it is not for
// production.
//
// One mutex guards everything, which makes each method atomic and gives
// races exactly one winner, the same guarantees Postgres gets from
// transactions and constraints.
package memory

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
)

type outboxEntry struct {
	msg  store.OutboxMessage
	sent bool
}

// Store implements store.Store in memory.
type Store struct {
	mu        sync.Mutex
	tenants   map[uuid.UUID]domain.Tenant
	slugs     map[string]uuid.UUID
	tasks     map[uuid.UUID]domain.Task
	outbox    []outboxEntry
	processed map[string]bool
	lastTime  time.Time

	// relayMu lets one relay pass run at a time without holding mu while
	// publishing, so slow publishes don't block API requests.
	relayMu sync.Mutex
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{
		tenants:   map[uuid.UUID]domain.Tenant{},
		slugs:     map[string]uuid.UUID{},
		tasks:     map[uuid.UUID]domain.Task{},
		processed: map[string]bool{},
	}
}

// now returns a strictly increasing UTC time, so (created_at, id) ordering
// follows insertion order like it does in Postgres. Callers hold mu.
func (s *Store) now() time.Time {
	t := time.Now().UTC().Truncate(time.Microsecond) // Postgres precision
	if !t.After(s.lastTime) {
		t = s.lastTime.Add(time.Microsecond)
	}
	s.lastTime = t
	return t
}

// CreateTenant implements store.Store.
func (s *Store) CreateTenant(_ context.Context, slug, name string) (domain.Tenant, domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.slugs[slug]; taken {
		return domain.Tenant{}, domain.Task{}, domain.ErrTenantAlreadyExists
	}
	now := domain.NewTimestamp(s.now())
	tenant := domain.Tenant{
		ID: uuid.New(), Slug: slug, Name: name, Status: domain.TenantProvisioning,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	task, err := s.addTask(tenant.ID, domain.TaskDeploy, nil)
	if err != nil {
		return domain.Tenant{}, domain.Task{}, err
	}
	s.tenants[tenant.ID] = tenant
	s.slugs[slug] = tenant.ID
	return tenant, cloneTask(task), nil
}

// UpdateTenant implements store.Store.
func (s *Store) UpdateTenant(_ context.Context, id uuid.UUID, name string, version int64) (domain.Tenant, domain.Task, error) {
	return s.transition(id, domain.TaskUpdate, domain.UpdatableFrom(), &version, &name)
}

// DeleteTenant implements store.Store.
func (s *Store) DeleteTenant(_ context.Context, id uuid.UUID) (domain.Tenant, domain.Task, error) {
	return s.transition(id, domain.TaskDestroy, domain.DeletableFrom(), nil, nil)
}

// transition checks the tenant's status and version, then moves it to the
// task's pending status and adds the task. The checks run in the same order
// as the Postgres store's, so both report the same error.
func (s *Store) transition(
	id uuid.UUID,
	typ domain.TaskType,
	allowedFrom []domain.TenantStatus,
	wantVersion *int64,
	requestedName *string,
) (domain.Tenant, domain.Task, error) {
	next, err := domain.PendingStatus(typ)
	if err != nil {
		return domain.Tenant{}, domain.Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tenant, ok := s.tenants[id]
	if !ok {
		return domain.Tenant{}, domain.Task{}, domain.ErrTenantNotFound
	}
	if !slices.Contains(allowedFrom, tenant.Status) {
		return domain.Tenant{}, domain.Task{}, fmt.Errorf("tenant is %s: %w", tenant.Status, domain.ErrTenantUpdateNotAllowed)
	}
	if wantVersion != nil && *wantVersion != tenant.Version {
		return domain.Tenant{}, domain.Task{}, fmt.Errorf("tenant is at version %d: %w", tenant.Version, domain.ErrTenantVersionConflict)
	}
	task, err := s.addTask(id, typ, requestedName)
	if err != nil {
		return domain.Tenant{}, domain.Task{}, err
	}
	tenant.Status = next
	tenant.Version++
	tenant.UpdatedAt = task.CreatedAt
	s.tenants[id] = tenant
	return tenant, cloneTask(task), nil
}

// addTask stores a task and its outbox message. Callers hold mu.
func (s *Store) addTask(tenantID uuid.UUID, typ domain.TaskType, requestedName *string) (domain.Task, error) {
	// Same safety net as the tasks_one_open_per_tenant index in Postgres.
	for _, t := range s.tasks {
		if t.TenantID == tenantID && !t.Status.IsTerminal() {
			return domain.Task{}, domain.ErrTenantUpdateNotAllowed
		}
	}
	now := domain.NewTimestamp(s.now())
	task := domain.Task{
		ID: uuid.New(), Type: typ, TenantID: tenantID, Status: domain.TaskAccepted,
		RequestedName: cloneString(requestedName), CreatedAt: now, UpdatedAt: now,
	}
	payload, err := json.Marshal(domain.NewTaskMessage(task))
	if err != nil {
		return domain.Task{}, fmt.Errorf("marshal task message: %w", err)
	}
	s.tasks[task.ID] = task
	s.outbox = append(s.outbox, outboxEntry{msg: store.OutboxMessage{
		ID: int64(len(s.outbox) + 1), TaskID: task.ID, Payload: payload,
	}})
	return task, nil
}

// GetTenant implements store.Store.
func (s *Store) GetTenant(_ context.Context, id uuid.UUID) (domain.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[id]
	if !ok {
		return domain.Tenant{}, domain.ErrTenantNotFound
	}
	return t, nil
}

// ListTenants implements store.Store.
func (s *Store) ListTenants(_ context.Context, f store.TenantFilter) (store.Page[domain.Tenant], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []domain.Tenant
	for _, t := range s.tenants {
		if f.Status == "" || t.Status == f.Status {
			rows = append(rows, t)
		}
	}
	return page(rows, f.Limit, f.After, func(t domain.Tenant) store.Cursor {
		return store.Cursor{CreatedAt: t.CreatedAt.Time, ID: t.ID}
	}), nil
}

// GetTask implements store.Store.
func (s *Store) GetTask(_ context.Context, id uuid.UUID) (domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	return cloneTask(t), nil
}

// ListTasks implements store.Store.
func (s *Store) ListTasks(_ context.Context, f store.TaskFilter) (store.Page[domain.Task], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []domain.Task
	for _, t := range s.tasks {
		if (f.TenantID == uuid.Nil || t.TenantID == f.TenantID) &&
			(f.Status == "" || t.Status == f.Status) &&
			(f.Type == "" || t.Type == f.Type) {
			rows = append(rows, cloneTask(t))
		}
	}
	return page(rows, f.Limit, f.After, func(t domain.Task) store.Cursor {
		return store.Cursor{CreatedAt: t.CreatedAt.Time, ID: t.ID}
	}), nil
}

// page sorts rows by (created_at, id), skips up to the cursor and cuts one page.
func page[T any](rows []T, limit int, after *store.Cursor, key func(T) store.Cursor) store.Page[T] {
	compare := func(a, b store.Cursor) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID.String(), b.ID.String())
	}
	slices.SortFunc(rows, func(a, b T) int { return compare(key(a), key(b)) })
	if after != nil {
		rows = slices.DeleteFunc(rows, func(r T) bool { return compare(key(r), *after) <= 0 })
	}
	limit = store.ClampLimit(limit)
	if len(rows) > limit+1 {
		rows = rows[:limit+1]
	}
	return store.Paginate(rows, limit, key)
}

// ApplyUpdate implements store.Store. Nothing is written until every check
// has passed, which is what rolling back a transaction does in Postgres.
func (s *Store) ApplyUpdate(_ context.Context, msg domain.UpdateMessage) (store.UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processed[msg.MessageID] {
		return store.UpdateResult{Outcome: store.OutcomeDuplicate}, nil
	}
	task, ok := s.tasks[msg.TaskID]
	if !ok {
		return store.UpdateResult{}, domain.ErrTaskNotFound
	}
	// Only move forward; a late in_progress after done is stale.
	if !slices.Contains(domain.StatusesBelow(msg.Status), task.Status) {
		s.processed[msg.MessageID] = true
		return store.UpdateResult{Outcome: store.OutcomeStale, Task: cloneTask(task)}, nil
	}

	now := domain.NewTimestamp(s.now())
	task.Status = msg.Status
	task.Error = nil
	if msg.Status == domain.TaskFailed {
		task.Error = cloneString(msg.Error)
	}
	task.UpdatedAt = now
	result := store.UpdateResult{Outcome: store.OutcomeApplied, Task: task}

	var tenant *domain.Tenant
	if task.Status.IsTerminal() {
		t := s.tenants[task.TenantID]
		next, err := domain.TenantStatusAfterTask(t.Status, task.Type, task.Status)
		if err != nil {
			return store.UpdateResult{}, err
		}
		t.Status = next
		if task.Type == domain.TaskUpdate && task.Status == domain.TaskDone && task.RequestedName != nil {
			t.Name = *task.RequestedName
		}
		t.Version++
		t.UpdatedAt = now
		tenant = &t
	}

	// Every check passed: commit.
	s.processed[msg.MessageID] = true
	s.tasks[task.ID] = task
	if tenant != nil {
		s.tenants[tenant.ID] = *tenant
		result.Tenant = tenant
	}
	result.Task = cloneTask(task)
	return result, nil
}

// RelayOutbox implements store.Store.
func (s *Store) RelayOutbox(ctx context.Context, limit int, publish func(context.Context, store.OutboxMessage) error) (int, error) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()

	s.mu.Lock()
	var batch []store.OutboxMessage
	for _, e := range s.outbox {
		if !e.sent && len(batch) < limit {
			batch = append(batch, e.msg)
		}
	}
	s.mu.Unlock()

	sent := 0
	var publishErr error
	for _, m := range batch {
		if err := publish(ctx, m); err != nil {
			publishErr = fmt.Errorf("publish outbox message %d: %w", m.ID, err)
			break
		}
		sent++
	}

	s.mu.Lock()
	for _, m := range batch[:sent] {
		s.outbox[m.ID-1].sent = true
	}
	s.mu.Unlock()
	return sent, publishErr
}

// UnsentOutboxCount implements store.Store.
func (s *Store) UnsentOutboxCount(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.outbox {
		if !e.sent {
			n++
		}
	}
	return n, nil
}

// Ping implements store.Store; memory is always reachable.
func (s *Store) Ping(context.Context) error { return nil }

// Close implements store.Store; there is nothing to release.
func (s *Store) Close() {}

// cloneTask copies the pointer fields so callers can't change stored tasks.
func cloneTask(t domain.Task) domain.Task {
	t.Error = cloneString(t.Error)
	t.RequestedName = cloneString(t.RequestedName)
	return t
}

func cloneString(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
