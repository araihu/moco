package operations

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// ServeDeploymentMetrics exposes readiness, local disk and backup health. Missing
// or unreadable status is explicit rather than an invented successful backup.
func ServeDeploymentMetrics(w http.ResponseWriter, r *http.Request, database, statusPath string, readiness interface{ Ping(context.Context) error }) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	ready := 0
	if readiness.Ping(ctx) == nil {
		ready = 1
	}
	_, _ = fmt.Fprintf(w, "# TYPE moco_ready gauge\nmoco_ready %d\n", ready)
	var disk unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(database), &disk); err == nil {
		_, _ = fmt.Fprintf(w, "# TYPE moco_data_disk_available_bytes gauge\nmoco_data_disk_available_bytes %g\n# TYPE moco_data_disk_size_bytes gauge\nmoco_data_disk_size_bytes %g\n", float64(disk.Bavail)*float64(disk.Bsize), float64(disk.Blocks)*float64(disk.Bsize))
	}
	if err := unix.Statfs(filepath.Dir(statusPath), &disk); err == nil {
		_, _ = fmt.Fprintf(w, "# TYPE moco_backup_disk_available_bytes gauge\nmoco_backup_disk_available_bytes %g\n# TYPE moco_backup_disk_size_bytes gauge\nmoco_backup_disk_size_bytes %g\n", float64(disk.Bavail)*float64(disk.Bsize), float64(disk.Blocks)*float64(disk.Bsize))
	}
	status, err := ReadBackupStatus(statusPath)
	readable, success, snapshot, lastSuccess := 0, 0, int64(0), int64(0)
	if err == nil {
		readable = 1
		if status.LastAttemptOK {
			success = 1
		}
		if !status.SnapshotAt.IsZero() {
			snapshot = status.SnapshotAt.Unix()
		}
		if !status.LastSuccess.IsZero() {
			lastSuccess = status.LastSuccess.Unix()
		}
	}
	_, _ = fmt.Fprintf(w, "# TYPE moco_backup_status_readable gauge\nmoco_backup_status_readable %d\n# TYPE moco_backup_last_attempt_success gauge\nmoco_backup_last_attempt_success %d\n# TYPE moco_backup_snapshot_timestamp_seconds gauge\nmoco_backup_snapshot_timestamp_seconds %d\n# TYPE moco_backup_last_success_timestamp_seconds gauge\nmoco_backup_last_success_timestamp_seconds %d\n# TYPE moco_backup_failures_total counter\nmoco_backup_failures_total %d\n# TYPE moco_backup_duration_seconds gauge\nmoco_backup_duration_seconds %g\n", readable, success, snapshot, lastSuccess, status.Failures, status.DurationSeconds)
}
