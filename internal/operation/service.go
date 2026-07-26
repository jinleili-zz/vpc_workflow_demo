package operation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidIdempotencyKey       = errors.New("INVALID_IDEMPOTENCY_KEY")
	ErrIdempotencyKeyReused        = errors.New("IDEMPOTENCY_KEY_REUSED")
	ErrInvalidResourceGeneration   = errors.New("INVALID_RESOURCE_GENERATION")
	ErrResourceSpecConflict        = errors.New("RESOURCE_SPEC_CONFLICT")
	ErrResourceOperationInProgress = errors.New("RESOURCE_OPERATION_IN_PROGRESS")
)

type BeginRequest struct {
	OperationID        string
	RootOperationID    string
	ParentOperationID  string
	OwnerService       string
	CallerScope        string
	RouteScope         string
	OperationType      string
	TargetScope        string
	IdempotencyKey     string
	RequestHashVersion int16
	Payload            any
	ResourceType       string
	ResourceID         string
	Generation         int64
}

type Service struct {
	repository *Repository
}

type dispatchLeaseContextKey struct{}

type dispatchLeaseIdentity struct {
	OperationID string
	LeaseOwner  string
}

// DispatchLease renews an operation's exclusive dispatch claim until the
// caller stores a fenced response or closes the lease.
type DispatchLease struct {
	service   *Service
	operation string
	owner     string
	cancel    context.CancelFunc
	done      chan struct{}
	lost      context.Context
	lostCause context.CancelCauseFunc
	once      sync.Once
}

func NewService(repository *Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Begin(ctx context.Context, request BeginRequest) (*Operation, Decision, error) {
	return s.begin(ctx, nil, request, false)
}

func (s *Service) BeginTarget(ctx context.Context, request BeginRequest) (*Operation, Decision, error) {
	return s.begin(ctx, nil, request, true)
}

func (s *Service) BeginTx(ctx context.Context, tx *sql.Tx, request BeginRequest) (*Operation, Decision, error) {
	if tx == nil {
		return nil, "", fmt.Errorf("operation transaction is required")
	}
	return s.begin(ctx, tx, request, false)
}

func (s *Service) BeginTargetTx(ctx context.Context, tx *sql.Tx, request BeginRequest) (*Operation, Decision, error) {
	if tx == nil {
		return nil, "", fmt.Errorf("operation transaction is required")
	}
	return s.begin(ctx, tx, request, true)
}

func (s *Service) begin(ctx context.Context, tx *sql.Tx, request BeginRequest, targetClaim bool) (*Operation, Decision, error) {
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return nil, "", err
	}
	if request.Generation < 0 {
		return nil, "", fmt.Errorf("%w: generation must be positive", ErrInvalidResourceGeneration)
	}
	if s.repository == nil {
		return nil, "", fmt.Errorf("operation repository is required")
	}
	version := request.RequestHashVersion
	if version == 0 {
		version = CanonicalHashVersion
	}
	hash, canonicalPayload, err := CanonicalHash(version, request.TargetScope, request.Payload)
	if err != nil {
		return nil, "", err
	}
	command := BeginCommand{
		OperationID:        request.OperationID,
		RootOperationID:    request.RootOperationID,
		ParentOperationID:  request.ParentOperationID,
		OwnerService:       request.OwnerService,
		CallerScope:        request.CallerScope,
		RouteScope:         request.RouteScope,
		OperationType:      request.OperationType,
		TargetScope:        request.TargetScope,
		IdempotencyKey:     request.IdempotencyKey,
		RequestHashVersion: version,
		RequestHash:        hash,
		RequestPayload:     canonicalPayload,
		ResourceType:       request.ResourceType,
		ResourceID:         request.ResourceID,
		Generation:         request.Generation,
	}
	if tx != nil {
		if targetClaim {
			return s.repository.BeginTargetTx(ctx, tx, command)
		}
		return s.repository.BeginTx(ctx, tx, command)
	}
	if targetClaim {
		return s.repository.BeginTarget(ctx, command)
	}
	return s.repository.Begin(ctx, command)
}

func (s *Service) Get(ctx context.Context, operationID string) (*Operation, error) {
	if s.repository == nil {
		return nil, fmt.Errorf("operation repository is required")
	}
	return s.repository.Get(ctx, operationID)
}

func (s *Service) ListRecoverableDispatch(ctx context.Context, ownerService string, limit int) ([]*Operation, error) {
	if s.repository == nil {
		return nil, fmt.Errorf("operation repository is required")
	}
	return s.repository.ListRecoverableDispatch(ctx, ownerService, limit)
}

func (s *Service) ListByStatus(ctx context.Context, ownerService string, status Status, limit int) ([]*Operation, error) {
	if s.repository == nil {
		return nil, fmt.Errorf("operation repository is required")
	}
	return s.repository.ListByStatus(ctx, ownerService, status, limit)
}

func (s *Service) AcquireDispatch(ctx context.Context, operationID string, leaseDuration time.Duration) (string, bool, error) {
	if s.repository == nil {
		return "", false, fmt.Errorf("operation repository is required")
	}
	leaseOwner := uuid.NewString()
	acquired, err := s.repository.AcquireDispatch(ctx, operationID, leaseOwner, leaseDuration)
	return leaseOwner, acquired, err
}

func (s *Service) ClaimDispatch(ctx context.Context, operationID string, leaseDuration time.Duration) (*DispatchLease, bool, error) {
	owner, acquired, err := s.AcquireDispatch(ctx, operationID, leaseDuration)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	renewCtx, cancel := context.WithCancel(context.Background())
	lostCtx, lostCause := context.WithCancelCause(context.Background())
	lease := &DispatchLease{service: s, operation: operationID, owner: owner, cancel: cancel, done: make(chan struct{}), lost: lostCtx, lostCause: lostCause}
	go lease.renew(renewCtx, leaseDuration)
	return lease, true, nil
}

func (l *DispatchLease) renew(ctx context.Context, duration time.Duration) {
	defer close(l.done)
	interval := duration / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(context.Background(), interval)
			renewed, err := l.service.repository.RenewDispatch(renewCtx, l.operation, l.owner, duration)
			cancel()
			if err != nil || !renewed {
				if err != nil {
					l.lostCause(fmt.Errorf("dispatch lease lost: %w", err))
				} else {
					l.lostCause(errors.New("dispatch lease ownership lost"))
				}
				return
			}
		}
	}
}

func (l *DispatchLease) Context(ctx context.Context) context.Context {
	if l == nil {
		return ctx
	}
	bound, cancel := context.WithCancelCause(ContextWithDispatchLease(ctx, l.operation, l.owner))
	context.AfterFunc(l.lost, func() { cancel(context.Cause(l.lost)) })
	return bound
}

func (l *DispatchLease) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.cancel()
		<-l.done
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = l.service.repository.ReleaseDispatch(releaseCtx, l.operation, l.owner)
		cancel()
		l.lostCause(context.Canceled)
	})
}

func ContextWithDispatchLease(ctx context.Context, operationID, leaseOwner string) context.Context {
	return context.WithValue(ctx, dispatchLeaseContextKey{}, dispatchLeaseIdentity{OperationID: operationID, LeaseOwner: leaseOwner})
}

func (s *Service) StoreResponse(ctx context.Context, operationID string, next Status, responseCode string, response any) (bool, error) {
	return s.storeResponse(ctx, operationID, next, responseCode, response, false)
}

func (s *Service) StoreResponseAndReleaseTarget(ctx context.Context, operationID string, next Status, responseCode string, response any) (bool, error) {
	if !next.Terminal() {
		return false, fmt.Errorf("target release requires a terminal operation response")
	}
	return s.storeResponse(ctx, operationID, next, responseCode, response, true)
}

func (s *Service) storeResponse(ctx context.Context, operationID string, next Status, responseCode string, response any, releaseTarget bool) (bool, error) {
	if s.repository == nil {
		return false, fmt.Errorf("operation repository is required")
	}
	op, err := s.repository.Get(ctx, operationID)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("marshal operation response: %w", err)
	}
	if op.Status == StatusDispatching {
		lease, ok := ctx.Value(dispatchLeaseContextKey{}).(dispatchLeaseIdentity)
		if !ok || lease.OperationID != operationID || lease.LeaseOwner == "" {
			return false, fmt.Errorf("dispatch response requires the current fenced lease")
		}
		return s.repository.storeResponseLease(ctx, operationID, lease.LeaseOwner, op.OperationType, next, responseCode, payload, releaseTarget)
	}
	return s.repository.storeResponseCAS(ctx, operationID, op.Version, op.OperationType, op.Status, next, responseCode, payload, releaseTarget)
}

func (s *Service) DeferDispatch(ctx context.Context, operationID string) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.DeferDispatch(ctx, operationID)
}

func (s *Service) DeferStatus(ctx context.Context, operationID string, status Status) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.DeferStatus(ctx, operationID, status)
}

func (s *Service) ReleaseTarget(ctx context.Context, ownerService, resourceType, targetScope string) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.ReleaseTarget(ctx, ownerService, resourceType, targetScope)
}

func (s *Service) ReleaseTargetTx(ctx context.Context, tx *sql.Tx, ownerService, resourceType, targetScope string) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.ReleaseTargetTx(ctx, tx, ownerService, resourceType, targetScope)
}

func (s *Service) MarkTargetRetiring(ctx context.Context, ownerService, resourceType, targetScope string) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.MarkTargetRetiring(ctx, ownerService, resourceType, targetScope)
}

func (s *Service) MarkTargetRetiringTx(ctx context.Context, tx *sql.Tx, ownerService, resourceType, targetScope string) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.MarkTargetRetiringTx(ctx, tx, ownerService, resourceType, targetScope)
}

func (s *Service) AssertTargetReleasable(ctx context.Context, ownerService, resourceType, targetScope string) error {
	if s.repository == nil {
		return fmt.Errorf("operation repository is required")
	}
	return s.repository.AssertTargetReleasable(ctx, ownerService, resourceType, targetScope)
}

func validateIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > 256 {
		return fmt.Errorf("%w: idempotency key must contain 1 to 256 printable ASCII characters", ErrInvalidIdempotencyKey)
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return fmt.Errorf("%w: idempotency key must contain 1 to 256 printable ASCII characters", ErrInvalidIdempotencyKey)
		}
	}
	return nil
}
