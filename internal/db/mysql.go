package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// MySQLConfig MySQL配置
type MySQLConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

var db *sql.DB

// InitMySQL 初始化MySQL连接
func InitMySQL(config *MySQLConfig) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		config.User,
		config.Password,
		config.Host,
		config.Port,
		config.Database,
	)

	var err error
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("打开MySQL连接失败: %v", err)
	}

	// 设置连接池参数
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)

	// 测试连接
	if err = db.Ping(); err != nil {
		return fmt.Errorf("MySQL连接失败: %v", err)
	}

	log.Println("✓ MySQL连接成功")
	return nil
}

// GetDB 获取数据库连接
func GetDB() *sql.DB {
	return db
}

// CloseMySQL 关闭MySQL连接
func CloseMySQL() error {
	if db != nil {
		return db.Close()
	}
	return nil
}
