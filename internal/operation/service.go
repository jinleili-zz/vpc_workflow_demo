package operation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrInvalidIdempotencyKey     = errors.New("INVALID_IDEMPOTENCY_KEY")
	ErrIdempotencyKeyReused      = errors.New("IDEMPOTENCY_KEY_REUSED")
	ErrInvalidResourceGeneration = errors.New("INVALID_RESOURCE_GENERATION")
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

func NewService(repository *Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Begin(ctx context.Context, request BeginRequest) (*Operation, Decision, error) {
	return s.begin(ctx, nil, request)
}

func (s *Service) BeginTx(ctx context.Context, tx *sql.Tx, request BeginRequest) (*Operation, Decision, error) {
	if tx == nil {
		return nil, "", fmt.Errorf("operation transaction is required")
	}
	return s.begin(ctx, tx, request)
}

func (s *Service) begin(ctx context.Context, tx *sql.Tx, request BeginRequest) (*Operation, Decision, error) {
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
		return s.repository.BeginTx(ctx, tx, command)
	}
	return s.repository.Begin(ctx, command)
}

func (s *Service) Get(ctx context.Context, operationID string) (*Operation, error) {
	if s.repository == nil {
		return nil, fmt.Errorf("operation repository is required")
	}
	return s.repository.Get(ctx, operationID)
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
