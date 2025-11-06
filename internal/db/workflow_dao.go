package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Workflow 工作流模型
type Workflow struct {
	ID           int64
	WorkflowID   string
	ResourceType string
	ResourceName string
	ResourceID   sql.NullString
	Region       string
	AZ           sql.NullString
	Status       string
	ErrorMessage sql.NullString
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateWorkflow 创建工作流
func CreateWorkflow(wf *Workflow) error {
	query := `INSERT INTO workflows (workflow_id, resource_type, resource_name, resource_id, region, az, status) 
	          VALUES (?, ?, ?, ?, ?, ?, ?)`

	result, err := db.Exec(query, wf.WorkflowID, wf.ResourceType, wf.ResourceName,
		wf.ResourceID, wf.Region, wf.AZ, wf.Status)
	if err != nil {
		return fmt.Errorf("创建工作流失败: %v", err)
	}

	wf.ID, _ = result.LastInsertId()
	return nil
}

// GetWorkflowByID 根据WorkflowID获取工作流
func GetWorkflowByID(workflowID string) (*Workflow, error) {
	query := `SELECT id, workflow_id, resource_type, resource_name, resource_id, region, az, status, 
	          error_message, created_at, updated_at FROM workflows WHERE workflow_id = ?`

	wf := &Workflow{}
	err := db.QueryRow(query, workflowID).Scan(
		&wf.ID, &wf.WorkflowID, &wf.ResourceType, &wf.ResourceName, &wf.ResourceID,
		&wf.Region, &wf.AZ, &wf.Status, &wf.ErrorMessage, &wf.CreatedAt, &wf.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询工作流失败: %v", err)
	}

	return wf, nil
}

// GetWorkflowByResourceName 根据资源名称获取工作流
func GetWorkflowByResourceName(resourceType, resourceName string) (*Workflow, error) {
	query := `SELECT id, workflow_id, resource_type, resource_name, resource_id, region, az, status, 
	          error_message, created_at, updated_at FROM workflows 
	          WHERE resource_type = ? AND resource_name = ? ORDER BY created_at DESC LIMIT 1`

	wf := &Workflow{}
	err := db.QueryRow(query, resourceType, resourceName).Scan(
		&wf.ID, &wf.WorkflowID, &wf.ResourceType, &wf.ResourceName, &wf.ResourceID,
		&wf.Region, &wf.AZ, &wf.Status, &wf.ErrorMessage, &wf.CreatedAt, &wf.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询工作流失败: %v", err)
	}

	return wf, nil
}

// UpdateWorkflowStatus 更新工作流状态
func UpdateWorkflowStatus(workflowID, status string, errorMsg *string) error {
	query := `UPDATE workflows SET status = ?, error_message = ?, updated_at = NOW() 
	          WHERE workflow_id = ?`

	_, err := db.Exec(query, status, errorMsg, workflowID)
	if err != nil {
		return fmt.Errorf("更新工作流状态失败: %v", err)
	}

	return nil
}

// CreateResourceMapping 创建资源映射
func CreateResourceMapping(resourceType, resourceName, resourceID, workflowID, region string, az *string) error {
	query := `INSERT INTO resource_mappings (resource_type, resource_name, resource_id, workflow_id, region, az) 
	          VALUES (?, ?, ?, ?, ?, ?) 
	          ON DUPLICATE KEY UPDATE resource_id = VALUES(resource_id), workflow_id = VALUES(workflow_id)`

	_, err := db.Exec(query, resourceType, resourceName, resourceID, workflowID, region, az)
	if err != nil {
		return fmt.Errorf("创建资源映射失败: %v", err)
	}

	return nil
}
