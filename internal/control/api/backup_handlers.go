package api

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/aipo-lenshow/EdgeNest/internal/control/backup"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// certsDir returns the on-disk directory holding PEM files. It mirrors how main
// wires the cert manager (filepath.Join(dataDir, "certs")).
func (h *Handler) certsDir() string {
	return filepath.Join(h.dataDir, "certs")
}

// SystemBackup streams a consistent snapshot of the panel as a download. It
// VACUUMs the SQLite database into a transactionally clean copy (WAL folded in,
// no live pause), then bundles that copy together with the certs/ directory
// into a gzip'd tar. Optionally the whole archive is encrypted with a passphrase
// (AES-256-GCM, argon2id key).
//
// The backup contains EVERYTHING the panel knows: inbounds, clients,
// subscriptions, routing/firewall, WARP, advanced + QUIC hardening, ALL settings
// (bot, timezone, log privacy, notify), daily traffic, audit log, AND the
// certificate PEM files. It does NOT capture the live engine config json (that
// is re-rendered from the DB on restore) or engine runtime logs.
//
// The restore is meant for the SAME machine (same public IP): inbound and
// subscription addresses are bound to this host's IP, so a different-IP machine
// needs them reconfigured regardless. The UI says so.
//
// POST /api/v1/system/backup   body: {"encrypt": bool, "password": string}
func (h *Handler) SystemBackup(c *gin.Context) {
	if h.dataDir == "" {
		core.Fail(c, http.StatusServiceUnavailable, "NO_DATADIR", "data dir not configured")
		return
	}
	var body struct {
		Encrypt  bool   `json:"encrypt"`
		Password string `json:"password"`
	}
	// Body is optional; an empty/absent body means an unencrypted backup.
	_ = c.ShouldBindJSON(&body)
	if body.Encrypt && body.Password == "" {
		core.Fail(c, http.StatusBadRequest, "NO_PASSWORD", "encryption requested but no password given")
		return
	}

	tmp := filepath.Join(h.dataDir, fmt.Sprintf("backup-%d.db", time.Now().UnixNano()))
	_ = os.Remove(tmp) // VACUUM INTO requires the target not to exist.
	if err := h.store.DB().Exec("VACUUM INTO ?", tmp).Error; err != nil {
		core.Fail(c, http.StatusInternalServerError, "BACKUP_FAILED", err.Error())
		return
	}
	defer os.Remove(tmp)

	stamp := time.Now().Format("20060102-150405")
	h.auditLog(c, "system.backup", "system", map[string]string{"encrypted": boolStr(body.Encrypt)})

	if !body.Encrypt {
		// Stream the tar.gz straight to the client — no full buffering needed.
		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Type", "application/gzip")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="edgenest-backup-%s.tar.gz"`, stamp))
		if err := backup.WriteTarGz(c.Writer, tmp, h.certsDir()); err != nil {
			// Headers are already sent; best we can do is abort the stream.
			c.Abort()
		}
		return
	}

	// Encrypted: the archive must be fully built before sealing (GCM is one-shot).
	// A panel DB + PEM files are a few MB, so in-memory is fine.
	var buf bytes.Buffer
	if err := backup.WriteTarGz(&buf, tmp, h.certsDir()); err != nil {
		core.Fail(c, http.StatusInternalServerError, "BACKUP_FAILED", err.Error())
		return
	}
	sealed, err := backup.Encrypt(buf.Bytes(), body.Password)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "ENCRYPT_FAILED", err.Error())
		return
	}
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="edgenest-backup-%s.tar.gz.enc"`, stamp))
	c.Data(http.StatusOK, "application/octet-stream", sealed)
}

// SystemRestore accepts an uploaded backup, decrypts/unpacks it, restores the
// certs to disk, stages the DB for a boot-time swap, and restarts the panel.
//
// Accepted formats (auto-detected by leading bytes, not file extension):
//   - encrypted archive (needs the "password" form field)
//   - plaintext tar.gz archive (db + certs)
//   - raw legacy .db (backward compatibility — no certs)
//
// We never hot-swap the open SQLite file. The DB is staged to
// <db>.restore-pending and applied at boot by store.ApplyPendingRestore; certs
// are written live (overwriting a same-domain cert is benign). Cert path columns
// are rebased onto this host's certsDir so a restore survives a different
// data-dir layout.
//
// POST /api/v1/system/restore   (multipart/form-data: "backup" file, optional "password")
func (h *Handler) SystemRestore(c *gin.Context) {
	if h.dataDir == "" {
		core.Fail(c, http.StatusServiceUnavailable, "NO_DATADIR", "data dir not configured")
		return
	}
	fh, err := c.FormFile("backup")
	if err != nil {
		core.Fail(c, http.StatusBadRequest, "NO_FILE", "no backup file uploaded (field 'backup')")
		return
	}
	if fh.Size > 256<<20 {
		core.Fail(c, http.StatusBadRequest, "TOO_LARGE", "backup file is implausibly large")
		return
	}
	password := c.PostForm("password")

	scratchArchive := filepath.Join(h.dataDir, fmt.Sprintf("restore-upload-%d", time.Now().UnixNano()))
	if err := c.SaveUploadedFile(fh, scratchArchive); err != nil {
		core.Fail(c, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	defer os.Remove(scratchArchive)

	raw, err := os.ReadFile(scratchArchive)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}

	// Where the staged plaintext DB will live until the boot-time swap.
	pending := h.store.DBPath() + store.PendingRestoreSuffix
	_ = os.Remove(pending)

	switch backup.Detect(raw) {
	case backup.KindSQLite:
		// Legacy raw DB: validate and stage as-is (no certs in this format).
		if err := validateBackupDB(scratchArchive); err != nil {
			core.Fail(c, http.StatusBadRequest, "INVALID_BACKUP", "file is not a valid EdgeNest backup: "+err.Error())
			return
		}
		if err := os.WriteFile(pending, raw, 0o600); err != nil {
			core.Fail(c, http.StatusInternalServerError, "STAGE_FAILED", err.Error())
			return
		}

	case backup.KindEncrypted, backup.KindGzip:
		var targz []byte
		if backup.Detect(raw) == backup.KindEncrypted {
			if password == "" {
				core.Fail(c, http.StatusBadRequest, "NEED_PASSWORD", "this backup is encrypted; a password is required")
				return
			}
			targz, err = backup.Decrypt(raw, password)
			if err != nil {
				core.Fail(c, http.StatusBadRequest, "BAD_PASSWORD", "incorrect password or corrupted backup")
				return
			}
		} else {
			targz = raw
		}

		// Unpack: DB to the pending slot, certs live to certsDir.
		if err := backup.Extract(targz, pending, h.certsDir()); err != nil {
			_ = os.Remove(pending)
			core.Fail(c, http.StatusBadRequest, "INVALID_BACKUP", "could not unpack archive: "+err.Error())
			return
		}
		if err := validateBackupDB(pending); err != nil {
			_ = os.Remove(pending)
			core.Fail(c, http.StatusBadRequest, "INVALID_BACKUP", "file is not a valid EdgeNest backup: "+err.Error())
			return
		}
		// Rebase cert path columns onto this host's certsDir (handles a different
		// data-dir between backup and restore). Best-effort: a failure here only
		// affects cert lookups, not the core restore.
		if err := rebaseCertPaths(pending, h.certsDir()); err != nil {
			core.Fail(c, http.StatusInternalServerError, "REBASE_FAILED", err.Error())
			return
		}

	default:
		core.Fail(c, http.StatusBadRequest, "INVALID_BACKUP", "unrecognized backup format")
		return
	}

	h.auditLog(c, "system.restore", "system", map[string]string{"size": intStr(int(fh.Size))})
	core.OK(c, gin.H{"restored": true, "restart_required": true})

	// Exit shortly after the response flushes so the supervisor restarts us and
	// store.ApplyPendingRestore folds the staged DB in at boot.
	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0)
	}()
}

// rebaseCertPaths rewrites certificates.cert_path / key_path in the staged DB so
// every path that lived under some ".../certs/" prefix now points at this host's
// certsDir. This keeps cert lookups valid when the backup was taken on a machine
// with a different data directory. Paths that don't contain a "/certs/" segment
// are left untouched.
func rebaseCertPaths(dbPath, certsDir string) error {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()
	if !db.Migrator().HasTable("certificates") {
		return nil // nothing to rebase
	}

	type certRow struct {
		ID       uint
		CertPath string
		KeyPath  string
	}
	var rows []certRow
	if err := db.Table("certificates").Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		newCert := backup.RebaseUnderCerts(r.CertPath, certsDir)
		newKey := backup.RebaseUnderCerts(r.KeyPath, certsDir)
		if newCert == r.CertPath && newKey == r.KeyPath {
			continue
		}
		if err := db.Table("certificates").Where("id = ?", r.ID).
			Updates(map[string]any{"cert_path": newCert, "key_path": newKey}).Error; err != nil {
			return err
		}
	}
	return nil
}

// validateBackupDB opens a file read-only and confirms it looks like an EdgeNest
// database (has the core tables). The connection is closed before returning.
func validateBackupDB(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	hdr := make([]byte, 16)
	n, _ := f.Read(hdr)
	_ = f.Close()
	if n < 16 || string(hdr[:15]) != "SQLite format 3" {
		return fmt.Errorf("not a SQLite database")
	}

	db, err := gorm.Open(sqlite.Open(path+"?mode=ro"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()
	for _, tbl := range []string{"admins", "settings", "inbounds"} {
		if !db.Migrator().HasTable(tbl) {
			return fmt.Errorf("missing table %q", tbl)
		}
	}
	return nil
}
