package store

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// AddDailyTraffic upserts the (date, email) bucket, adding the given delta to
// the day's running total. Called by the traffic poller alongside
// AddUserTraffic with the same per-tick delta, so the daily buckets and the
// cumulative Client counters stay consistent. A no-op for a zero delta.
func (s *Store) AddDailyTraffic(date, email string, up, down int64) error {
	if up == 0 && down == 0 {
		return nil
	}
	now := time.Now().Unix()
	row := model.TrafficDaily{Date: date, Email: email, Up: up, Down: down, UpdatedAt: now}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "date"}, {Name: "email"}},
		DoUpdates: clause.Assignments(map[string]any{
			"up":         gorm.Expr("up + ?", up),
			"down":       gorm.Expr("down + ?", down),
			"updated_at": now,
		}),
	}).Create(&row).Error
}

// UserTrafficSince sums one user's up/down bytes over all daily buckets on or
// after sinceDate (YYYY-MM-DD, server-local). Pass the first day of the month
// for month-to-date. Lexical date comparison is valid for zero-padded dates.
func (s *Store) UserTrafficSince(email, sinceDate string) (up, down int64, err error) {
	var r struct{ Up, Down int64 }
	err = s.db.Model(&model.TrafficDaily{}).
		Select("COALESCE(SUM(up),0) AS up, COALESCE(SUM(down),0) AS down").
		Where("email = ? AND date >= ?", email, sinceDate).
		Scan(&r).Error
	return r.Up, r.Down, err
}

// ServerTrafficSince sums all users' up/down bytes over all daily buckets on or
// after sinceDate. Pass the first day of the month for server month-to-date.
func (s *Store) ServerTrafficSince(sinceDate string) (up, down int64, err error) {
	var r struct{ Up, Down int64 }
	err = s.db.Model(&model.TrafficDaily{}).
		Select("COALESCE(SUM(up),0) AS up, COALESCE(SUM(down),0) AS down").
		Where("date >= ?", sinceDate).
		Scan(&r).Error
	return r.Up, r.Down, err
}

// PruneDailyBefore deletes buckets older than date (exclusive) to bound table
// growth. Callers decide retention (e.g. drop > ~13 months of history).
func (s *Store) PruneDailyBefore(date string) error {
	return s.db.Where("date < ?", date).Delete(&model.TrafficDaily{}).Error
}
