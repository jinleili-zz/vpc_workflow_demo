package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type MySQLConfig struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

var globalDB *sql.DB

func InitMySQL(cfg *MySQLConfig) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database,
	)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("打开数据库连接失败: %v", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库连接测试失败: %v", err)
	}

	globalDB = db
	log.Printf("[MySQL] 数据库连接成功: %s:%d/%s", cfg.Host, cfg.Port, cfg.Database)

	return nil
}

func GetDB() *sql.DB {
	return globalDB
}

func LoadMySQLConfig() *MySQLConfig {
	return &MySQLConfig{
		Host:     getEnv("MYSQL_HOST", "localhost"),
		Port:     getEnvInt("MYSQL_PORT", 3306),
		Database: getEnv("MYSQL_DATABASE", "nsp_default"),
		User:     getEnv("MYSQL_USER", "root"),
		Password: getEnv("MYSQL_PASSWORD", "root"),
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	var result int
	fmt.Sscanf(value, "%d", &result)
	return result
}

func RunMigrations(db *sql.DB, migrationDir string) error {
	log.Printf("[MySQL] 执行数据库迁移: %s", migrationDir)
	
	files, err := os.ReadDir(migrationDir)
	if err != nil {
		return fmt.Errorf("读取迁移目录失败: %v", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		sqlFile := migrationDir + "/" + file.Name()
		log.Printf("[MySQL] 执行迁移脚本: %s", file.Name())

		content, err := os.ReadFile(sqlFile)
		if err != nil {
			return fmt.Errorf("读取SQL文件失败: %v", err)
		}

		statements := splitSQLStatements(string(content))
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			_, err = db.Exec(stmt)
			if err != nil {
				return fmt.Errorf("执行SQL失败 (%s): %v", file.Name(), err)
			}
		}
	}

	log.Printf("[MySQL] 数据库迁移完成")
	return nil
}

func splitSQLStatements(content string) []string {
	lines := strings.Split(content, "\n")
	var statements []string
	var currentStmt strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}

		currentStmt.WriteString(line)
		currentStmt.WriteString("\n")

		if strings.HasSuffix(trimmed, ";") {
			statements = append(statements, currentStmt.String())
			currentStmt.Reset()
		}
	}

	if currentStmt.Len() > 0 {
		statements = append(statements, currentStmt.String())
	}

	return statements
}

func Close() error {
	if globalDB != nil {
		return globalDB.Close()
	}
	return nil
}