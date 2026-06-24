package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// engineLogFiles are the per-engine log files written under DataDir. Both are
// opened O_APPEND by the running engine; truncating to 0 is safe while the fd
// is held (the next append writes at offset 0, no sparse hole) so no restart is
// needed and sing-box.json is never touched.
var engineLogFiles = []string{"sing-box.log", "xray.log"}

// ClearLogs truncates the engine log files in place. A privacy / housekeeping
// action: self-hosters who don't want their users' source IPs (or any history)
// sitting on disk can wipe the logs without restarting the proxy engines.
//
// Best-effort per file: a missing file is not an error (the engine may not have
// started yet, or xray may be unused). Returns how many bytes were reclaimed.
//
// POST /api/v1/logs/clear
func (h *Handler) ClearLogs(c *gin.Context) {
	var cleared int64
	var wiped []string
	for _, name := range engineLogFiles {
		path := filepath.Join(h.dataDir, name)
		fi, err := os.Stat(path)
		if err != nil {
			continue // missing file: nothing to clear
		}
		if err := os.Truncate(path, 0); err != nil {
			core.Fail(c, http.StatusInternalServerError, "TRUNCATE_FAILED", err.Error())
			return
		}
		cleared += fi.Size()
		wiped = append(wiped, name)
	}
	h.auditLog(c, "logs.clear", "logs", map[string]string{"bytes": strconv.FormatInt(cleared, 10)})
	core.OK(c, gin.H{"cleared_bytes": cleared, "files": wiped})
}

// LogsSize reports the combined on-disk size of the engine log files so the
// panel can show how much is sitting there next to the "clear logs" button.
//
// GET /api/v1/logs/size
func (h *Handler) LogsSize(c *gin.Context) {
	var total int64
	for _, name := range engineLogFiles {
		if fi, err := os.Stat(filepath.Join(h.dataDir, name)); err == nil {
			total += fi.Size()
		}
	}
	core.OK(c, gin.H{"total_bytes": total})
}
