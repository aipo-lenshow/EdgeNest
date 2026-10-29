package store

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// CDNSample is one address's result in a client's probe round.
type CDNSample struct {
	IP string
	OK bool
	Ms int64
}

// AddCDNSamples folds one accepted probe round into the day's per-network
// buckets.
func (s *Store) AddCDNSamples(day, netKey, netLabel string, samples []CDNSample) error {
	now := time.Now().Unix()
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, sm := range samples {
			var ok, ms int64
			if sm.OK {
				ok, ms = 1, sm.Ms
			}
			row := model.CDNStatDaily{Day: day, NetKey: netKey, IP: sm.IP, NetLabel: netLabel,
				Samples: 1, OK: ok, MsSum: ms, UpdatedAt: now}
			err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "day"}, {Name: "net_key"}, {Name: "ip"}},
				DoUpdates: clause.Assignments(map[string]any{
					"samples":    gorm.Expr("samples + 1"),
					"ok":         gorm.Expr("ok + ?", ok),
					"ms_sum":     gorm.Expr("ms_sum + ?", ms),
					"net_label":  netLabel,
					"updated_at": now,
				}),
			}).Create(&row).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// CDNStatTotal is one network × address summed over a window.
type CDNStatTotal struct {
	NetKey    string
	NetLabel  string
	IP        string
	Samples   int64
	OK        int64
	MsSum     int64
	UpdatedAt int64
}

// CDNStatsSince sums the buckets on or after sinceDay (YYYY-MM-DD); pass an
// empty netKey for every network.
func (s *Store) CDNStatsSince(sinceDay, netKey string) ([]CDNStatTotal, error) {
	q := s.db.Model(&model.CDNStatDaily{}).
		Select("net_key, max(net_label) as net_label, ip, sum(samples) as samples, sum(ok) as ok, sum(ms_sum) as ms_sum, max(updated_at) as updated_at").
		Where("day >= ?", sinceDay)
	if netKey != "" {
		q = q.Where("net_key = ?", netKey)
	}
	var out []CDNStatTotal
	err := q.Group("net_key, ip").Scan(&out).Error
	return out, err
}

// DeleteCDNStats deletes a network's pooled results, or one address's on it
// when ip is not empty, across all days. Returns rows removed.
func (s *Store) DeleteCDNStats(netKey, ip string) (int64, error) {
	q := s.db.Where("net_key = ?", netKey)
	if ip != "" {
		q = q.Where("ip = ?", ip)
	}
	res := q.Delete(&model.CDNStatDaily{})
	return res.RowsAffected, res.Error
}

// PruneCDNStatsBefore deletes buckets older than day (exclusive).
func (s *Store) PruneCDNStatsBefore(day string) (int64, error) {
	res := s.db.Where("day < ?", day).Delete(&model.CDNStatDaily{})
	return res.RowsAffected, res.Error
}
