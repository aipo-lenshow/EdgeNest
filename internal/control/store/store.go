// Package store is the data-access layer. It wraps GORM and owns DB lifecycle
// (open + migrate) plus repository helpers used by the control plane.
package store

import (
	"errors"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store holds the GORM DB handle.
type Store struct {
	db   *gorm.DB
	path string
}

// Open opens (creating if needed) the SQLite database at path and runs
// AutoMigrate for all models.
func Open(path string) (*Store, error) {
	// Silent: our repository methods handle ErrRecordNotFound explicitly, so
	// GORM's default "record not found" warnings would just be noise.
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

// DB exposes the underlying GORM handle for packages that need direct access.
func (s *Store) DB() *gorm.DB { return s.db }

// DBPath returns the filesystem path of the SQLite database (used by the
// backup/restore handlers to locate the staging file).
func (s *Store) DBPath() string { return s.path }

// ---- Setting helpers ----

// GetSetting returns the value for key, or "" if not present.
func (s *Store) GetSetting(key string) (string, error) {
	var st model.Setting
	err := s.db.First(&st, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return st.Value, nil
}

// SetSetting upserts a key-value setting.
func (s *Store) SetSetting(key, value string) error {
	st := model.Setting{Key: key, Value: value}
	return s.db.Save(&st).Error
}

// ---- Admin helpers ----

// AdminCount returns the number of admins (used to detect first run).
func (s *Store) AdminCount() (int64, error) {
	var n int64
	err := s.db.Model(&model.Admin{}).Count(&n).Error
	return n, err
}

// CreateAdmin inserts a new admin.
func (s *Store) CreateAdmin(a *model.Admin) error {
	now := time.Now().Unix()
	a.CreatedAt, a.UpdatedAt = now, now
	return s.db.Create(a).Error
}

// FirstAdmin returns the single admin account (Lite mode provisions exactly
// one on first run). Used by the CLI management menu for status display and
// password reset, where the username isn't known ahead of time. Returns
// gorm.ErrRecordNotFound when no admin exists yet (pre-bootstrap).
func (s *Store) FirstAdmin() (*model.Admin, error) {
	var a model.Admin
	if err := s.db.Order("id asc").First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// GetAdminByUsername fetches an admin by username.
func (s *Store) GetAdminByUsername(username string) (*model.Admin, error) {
	var a model.Admin
	if err := s.db.First(&a, "username = ?", username).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// UpdateAdmin saves admin changes.
func (s *Store) UpdateAdmin(a *model.Admin) error {
	a.UpdatedAt = time.Now().Unix()
	return s.db.Save(a).Error
}

// ---- Node helpers ----

// EnsureLocalNode upserts the single local node used in Lite mode and returns it.
func (s *Store) EnsureLocalNode() (*model.Node, error) {
	var n model.Node
	err := s.db.First(&n, "is_local = ?", true).Error
	if err == nil {
		return &n, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().Unix()
	n = model.Node{
		Name:      "local",
		Role:      "standalone",
		Status:    "online",
		IsLocal:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Create(&n).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

// GetLocalNode returns the local node (Lite).
func (s *Store) GetLocalNode() (*model.Node, error) {
	var n model.Node
	if err := s.db.First(&n, "is_local = ?", true).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

// UpdateNode persists changes to an existing node row. Used by bootstrap to
// backfill PublicIP after auto-detection.
func (s *Store) UpdateNode(n *model.Node) error {
	n.UpdatedAt = time.Now().Unix()
	return s.db.Save(n).Error
}

// ListNodes returns all nodes.
func (s *Store) ListNodes() ([]model.Node, error) {
	var ns []model.Node
	err := s.db.Order("id asc").Find(&ns).Error
	return ns, err
}
