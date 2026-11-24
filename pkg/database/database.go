// Package database opens the gorm connection used for migration state. It
// mirrors enumdns: a URI scheme selects the driver (sqlite/postgres/mysql).
package database

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/helviojunior/mail_migration/pkg/models"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Connect opens the database at uri and runs AutoMigrate for all state models.
//
// Supported URIs:
//
//	sqlite://path/to/file.db   or   sqlite:///abs/path.db
//	postgres://user:pass@host:5432/dbname
//	mysql://user:pass@host:3306/dbname
func Connect(uri string, debug bool) (*gorm.DB, error) {
	var dialector gorm.Dialector

	switch {
	case strings.HasPrefix(uri, "sqlite://"):
		path := strings.TrimPrefix(uri, "sqlite://")
		path = strings.TrimPrefix(path, "/") // tolerate sqlite:/// form -> keep leading slash below
		if strings.HasPrefix(uri, "sqlite:///") {
			path = "/" + path
		}
		dialector = sqlite.Open(path)
	case strings.HasPrefix(uri, "postgres://"), strings.HasPrefix(uri, "postgresql://"):
		dialector = postgres.Open(uri)
	case strings.HasPrefix(uri, "mysql://"):
		dialector = mysql.Open(strings.TrimPrefix(uri, "mysql://"))
	default:
		return nil, fmt.Errorf("unsupported database URI: %q", uri)
	}

	logLevel := logger.Silent
	if debug {
		logLevel = logger.Info
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:                                   logger.Default.LogMode(logLevel),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(models.All()...); err != nil {
		return nil, err
	}

	return db, nil
}
