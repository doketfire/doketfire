package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/doketfire/doketfire/internal/api"
	"github.com/doketfire/doketfire/internal/storage"
)

// Compact rebuilds data.db from a consistent Bbolt snapshot. All HTTP requests
// are blocked while the store is closed and the file is swapped.
func (a *App) Compact(ctx context.Context) (*api.MaintenanceReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Config.ReadOnly {
		return nil, storage.ErrReadOnly
	}
	release := a.gate.beginMaintenance()
	defer release()
	current := a.currentServices()
	if current.store == nil {
		return nil, storage.ErrClosed
	}
	before := current.store.Size()
	tmp, err := os.CreateTemp(a.Config.Dir, "compact-*.db")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := current.store.Backup(tmp); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	closeServices(current)
	dataPath := current.store.Path()
	backup := a.backupPath(dataPath, "pre-compact")
	if err := os.Rename(dataPath, backup); err != nil {
		a.recoverServices()
		return nil, err
	}
	if err := os.Rename(tmpName, dataPath); err != nil {
		_ = os.Rename(backup, dataPath)
		a.recoverServices()
		return nil, err
	}
	after, err := a.reopenAfterSwap(dataPath, backup, "pre-compact")
	if err != nil {
		return nil, err
	}
	return &api.MaintenanceReport{Before: before, After: after, Backup: backup}, nil
}

// Restore installs an uploaded Bbolt database. The uploaded file is validated
// before the live store is closed. On any failure the previous database and
// the previous service generation are restored.
func (a *App) Restore(ctx context.Context, src io.Reader) (*api.MaintenanceReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Config.ReadOnly {
		return nil, storage.ErrReadOnly
	}
	if src == nil {
		return nil, errors.New("app: restore source is required")
	}
	release := a.gate.beginMaintenance()
	defer release()
	current := a.currentServices()
	if current.store == nil {
		return nil, storage.ErrClosed
	}
	before := current.store.Size()
	tmp, err := os.CreateTemp(a.Config.Dir, "restore-*.db")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := validateBboltFile(tmpName); err != nil {
		return nil, fmt.Errorf("invalid database file: %w", err)
	}
	closeServices(current)
	dataPath := current.store.Path()
	backup := a.backupPath(dataPath, "pre-restore")
	if err := os.Rename(dataPath, backup); err != nil {
		a.recoverServices()
		return nil, err
	}
	if err := os.Rename(tmpName, dataPath); err != nil {
		_ = os.Rename(backup, dataPath)
		a.recoverServices()
		return nil, err
	}
	after, err := a.reopenAfterSwap(dataPath, backup, "pre-restore")
	if err != nil {
		return nil, err
	}
	return &api.MaintenanceReport{Before: before, After: after, Backup: backup}, nil
}

func (a *App) backupPath(dataPath, label string) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	return filepath.Join(a.Config.Dir, filepath.Base(dataPath)+"."+label+"-"+stamp)
}

// reopenAfterSwap builds a new generation from the swapped file. If that
// fails, it puts the backup back and brings the previous generation online.
func (a *App) reopenAfterSwap(dataPath, backup, label string) (int64, error) {
	next, err := a.buildServices()
	if err == nil {
		a.installServices(next)
		st, statErr := os.Stat(dataPath)
		if statErr != nil {
			return 0, statErr
		}
		return st.Size(), nil
	}
	failed := dataPath + ".failed-" + label + "-" + time.Now().UTC().Format("20060102-150405")
	_ = os.Rename(dataPath, failed)
	if restoreErr := os.Rename(backup, dataPath); restoreErr != nil {
		return 0, fmt.Errorf("reopen after %s: %w (rollback failed: %v)", label, err, restoreErr)
	}
	a.recoverServices()
	return 0, fmt.Errorf("reopen after %s: %w", label, err)
}

// recoverServices brings a fresh generation online after a failed swap. The
// caller must already have restored the old database file.
func (a *App) recoverServices() {
	services, err := a.buildServices()
	if err != nil {
		if a.Logger != nil {
			a.Logger.Error("could not restore services after maintenance failure", "error", err)
		}
		return
	}
	a.installServices(services)
}

func validateBboltFile(path string) error {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("_meta")) == nil || tx.Bucket([]byte("_collections")) == nil {
			return errors.New("missing Doketfire system buckets")
		}
		return nil
	})
}
