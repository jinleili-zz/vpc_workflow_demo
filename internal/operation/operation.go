// Package operation 实现设计文档（docs/architecture/idempotency-analysis-and-design.md）
// 第 9.2/10.1/11.1 节定义的统一 Operation 幂等模型：
// 同一 (owner_service, caller_scope, route_scope, idempotency_key) 最多对应一个 Operation；
// 相同 Key + 相同请求重放第一次响应；相同 Key + 不同请求返回冲突。
package operation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Operation 状态
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// 统一业务码（设计文档 11.1 节）；成功固定为 "0"，与 Saga executor 的成功判定兼容。
const (
	CodeSuccess               = "0"
	CodeIdempotencyKeyReused  = "IDEMPOTENCY_KEY_REUSED"
	CodeOperationUnavailable  = "OPERATION_UNAVAILABLE"
	CodeInvalidRequest        = "INVALID_REQUEST"
	CodeInternalError         = "INTERNAL_ERROR"
	CodeResourceAlreadyExists = "RESOURCE_ALREADY_EXISTS"
)

// ErrRequestConflict 表示相同幂等键携带了不同的规范化请求（设计文档规则 3：返回 409）。
var ErrRequestConflict = errors.New("idempotency key reused with a different request")

// Operation 一次业务操作的持久化记录。
type Operation struct {
	OperationID       string          `json:"operation_id"`
	RootOperationID   string          `json:"root_operation_id"`
	ParentOperationID string          `json:"parent_operation_id,omitempty"`
	OwnerService      string          `json:"owner_service"`
	CallerScope       string          `json:"caller_scope"`
	RouteScope        string          `json:"route_scope"`
	OperationType     string          `json:"operation_type"`
	IdempotencyKey    string          `json:"idempotency_key"`
	RequestHash       string          `json:"request_hash"`
	RequestPayload    json.RawMessage `json:"request_payload"`
	Status            string          `json:"status"`
	ResponseCode      int             `json:"response_code,omitempty"`
	ResponsePayload   json.RawMessage `json:"response_payload,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
}

// IsTerminal 报告 Operation 是否已进入终态（响应可安全重放）。
func (o *Operation) IsTerminal() bool {
	return o.Status == StatusSucceeded || o.Status == StatusFailed
}

// BeginCommand 创建/获取 Operation 的入参。
type BeginCommand struct {
	CallerScope       string // 调用方作用域；认证关闭的过渡期使用固定命名空间，不得使用来源 IP
	RouteScope        string // 规范化 HTTP 方法 + 路由模板，如 "POST /api/v1/vpc"
	OperationType     string // 如 create_vpc / create_subnet / create_pccn
	IdempotencyKey    string // 为空时由服务生成（兼容期行为，无法保护响应丢失后的客户端重试）
	Request           any    // 规范化后的请求结构，用于计算 request_hash 与保存审计载荷
	RootOperationID   string // 可选；为空时使用自身 operation_id
	ParentOperationID string // 可选
}

// Service 提供 Operation 的创建、完成与查询能力。
type Service struct {
	db           *sql.DB
	ownerService string
}

// NewService 创建 Operation Service。ownerService 例如 "top-nsp-vpc" / "az-nsp-vpc"。
func NewService(db *sql.DB, ownerService string) *Service {
	return &Service{db: db, ownerService: ownerService}
}

// HashRequest 对规范化请求结构计算 SHA-256。Go 的 json.Marshal 对结构体字段
// 按声明顺序输出，因此同一请求类型的序列化结果是稳定的。
func HashRequest(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(fmt.Sprintf("%v", v))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Begin 以唯一约束作为并发线性化点创建 Operation。
// 返回值 (op, true, nil) 表示本次调用赢得创建权，调用方必须继续执行并在结束后调用 Complete；
// 返回值 (op, false, nil) 表示相同 Key + 相同请求的 Operation 已存在；
// ErrRequestConflict 表示相同 Key 但请求指纹不同。
func (s *Service) Begin(ctx context.Context, cmd BeginCommand) (*Operation, bool, error) {
	if cmd.IdempotencyKey == "" {
		cmd.IdempotencyKey = "auto-" + uuid.NewString()
	}

	requestPayload, err := json.Marshal(cmd.Request)
	if err != nil {
		return nil, false, fmt.Errorf("序列化请求载荷失败: %w", err)
	}

	op := &Operation{
		OperationID:       uuid.NewString(),
		RootOperationID:   cmd.RootOperationID,
		ParentOperationID: cmd.ParentOperationID,
		OwnerService:      s.ownerService,
		CallerScope:       cmd.CallerScope,
		RouteScope:        cmd.RouteScope,
		OperationType:     cmd.OperationType,
		IdempotencyKey:    cmd.IdempotencyKey,
		RequestHash:       HashRequest(cmd.Request),
		RequestPayload:    requestPayload,
		Status:            StatusRunning,
	}
	if op.RootOperationID == "" {
		op.RootOperationID = op.OperationID
	}

	insertQuery := `
		INSERT INTO orchestration_operations (
			operation_id, root_operation_id, parent_operation_id, owner_service,
			caller_scope, route_scope, operation_type, idempotency_key,
			request_hash, request_payload, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
		ON CONFLICT (owner_service, caller_scope, route_scope, idempotency_key) DO NOTHING
		RETURNING operation_id, created_at, updated_at
	`
	err = s.db.QueryRowContext(ctx, insertQuery,
		op.OperationID, op.RootOperationID, nullString(op.ParentOperationID), op.OwnerService,
		op.CallerScope, op.RouteScope, op.OperationType, op.IdempotencyKey,
		op.RequestHash, string(op.RequestPayload), op.Status,
	).Scan(&op.OperationID, &op.CreatedAt, &op.UpdatedAt)
	if err == nil {
		return op, true, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, fmt.Errorf("创建Operation失败: %w", err)
	}

	// 唯一键冲突：加载已有 Operation 并校验请求指纹
	existing, err := s.getByKey(ctx, cmd.CallerScope, cmd.RouteScope, cmd.IdempotencyKey)
	if err != nil {
		return nil, false, fmt.Errorf("加载已有Operation失败: %w", err)
	}
	if existing.RequestHash != op.RequestHash {
		return existing, false, ErrRequestConflict
	}
	return existing, false, nil
}

// Complete 写入终态与可重放响应。HTTP 状态码 < 400 记为 succeeded，否则记为 failed。
func (s *Service) Complete(ctx context.Context, operationID, status string, responseCode int, responsePayload []byte) error {
	query := `
		UPDATE orchestration_operations
		SET status = $2, response_code = $3, response_payload = $4::jsonb,
		    completed_at = NOW(), updated_at = NOW()
		WHERE operation_id = $1
	`
	var payload any
	if len(responsePayload) > 0 {
		payload = string(responsePayload)
	}
	_, err := s.db.ExecContext(ctx, query, operationID, status, responseCode, payload)
	return err
}

// GetByID 按 operation_id 查询。
func (s *Service) GetByID(ctx context.Context, operationID string) (*Operation, error) {
	query := `
		SELECT operation_id, root_operation_id, parent_operation_id, owner_service,
		       caller_scope, route_scope, operation_type, idempotency_key,
		       request_hash, request_payload::text, status, response_code,
		       response_payload::text, error_code, error_message,
		       created_at, updated_at, completed_at
		FROM orchestration_operations
		WHERE operation_id = $1
	`
	op, err := s.scanOne(s.db.QueryRowContext(ctx, query, operationID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("Operation不存在: %s", operationID)
	}
	return op, err
}

// WaitTerminal 轮询等待 Operation 进入终态，用于并发相同 Key 请求等待首个请求完成。
func (s *Service) WaitTerminal(ctx context.Context, operationID string, timeout time.Duration) (*Operation, error) {
	deadline := time.Now().Add(timeout)
	for {
		op, err := s.GetByID(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if op.IsTerminal() {
			return op, nil
		}
		if time.Now().After(deadline) {
			return op, fmt.Errorf("Operation %s 在等待期内未完成", operationID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *Service) getByKey(ctx context.Context, callerScope, routeScope, key string) (*Operation, error) {
	query := `
		SELECT operation_id, root_operation_id, parent_operation_id, owner_service,
		       caller_scope, route_scope, operation_type, idempotency_key,
		       request_hash, request_payload::text, status, response_code,
		       response_payload::text, error_code, error_message,
		       created_at, updated_at, completed_at
		FROM orchestration_operations
		WHERE owner_service = $1 AND caller_scope = $2 AND route_scope = $3 AND idempotency_key = $4
	`
	return s.scanOne(s.db.QueryRowContext(ctx, query, s.ownerService, callerScope, routeScope, key))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Service) scanOne(row rowScanner) (*Operation, error) {
	op := &Operation{}
	var parentID, reqPayload, respPayload, errCode, errMsg sql.NullString
	var respCode sql.NullInt64
	var completedAt sql.NullTime

	err := row.Scan(
		&op.OperationID, &op.RootOperationID, &parentID, &op.OwnerService,
		&op.CallerScope, &op.RouteScope, &op.OperationType, &op.IdempotencyKey,
		&op.RequestHash, &reqPayload, &op.Status, &respCode,
		&respPayload, &errCode, &errMsg,
		&op.CreatedAt, &op.UpdatedAt, &completedAt,
	)
	if err != nil {
		return nil, err
	}
	op.ParentOperationID = parentID.String
	if reqPayload.Valid {
		op.RequestPayload = json.RawMessage(reqPayload.String)
	}
	if respCode.Valid {
		op.ResponseCode = int(respCode.Int64)
	}
	if respPayload.Valid {
		op.ResponsePayload = json.RawMessage(respPayload.String)
	}
	op.ErrorCode = errCode.String
	op.ErrorMessage = errMsg.String
	if completedAt.Valid {
		t := completedAt.Time
		op.CompletedAt = &t
	}
	return op, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
