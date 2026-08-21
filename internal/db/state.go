package db

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/shaktsin/biewer/internal/model"
)

const (
	snapshotKey  = "dashboard/snapshot/v1"
	usageKey     = "dashboard/usage-sources/v1"
	telemetryKey = "dashboard/telemetry-sessions/v1"
)

type stateBackend interface {
	Put(key string, value []byte) error
	Get(key string) ([]byte, error)
	Close() error
	Name() string
}

// StateDB persists the hot dashboard snapshot and token-source index. The
// implementation is RocksDB when built with `-tags rocksdb`; portable builds
// use the same API with atomic files so development and tests need no native
// library.
type StateDB struct {
	mu      sync.Mutex
	backend stateBackend
}

func OpenState(dir string) (*StateDB, error) {
	backend, err := openStateBackend(filepath.Join(dir, "dashboard.rocksdb"), filepath.Join(dir, "state"))
	if err != nil {
		return nil, err
	}
	return &StateDB{backend: backend}, nil
}

func (d *StateDB) Name() string { return d.backend.Name() }

func (d *StateDB) PutSnapshot(snapshot model.Snapshot) error {
	return d.putJSON(snapshotKey, snapshot)
}

func (d *StateDB) Snapshot() (model.Snapshot, error) {
	var snapshot model.Snapshot
	err := d.getJSON(snapshotKey, &snapshot)
	return snapshot, err
}

func (d *StateDB) PutUsageSources(sources []model.UsageSource) error {
	return d.putJSON(usageKey, sources)
}

func (d *StateDB) UsageSources() ([]model.UsageSource, error) {
	var sources []model.UsageSource
	err := d.getJSON(usageKey, &sources)
	return sources, err
}

func (d *StateDB) PutTelemetrySessions(sessions []model.TelemetrySession) error {
	return d.putJSON(telemetryKey, sessions)
}

func (d *StateDB) TelemetrySessions() ([]model.TelemetrySession, error) {
	var sessions []model.TelemetrySession
	err := d.getJSON(telemetryKey, &sessions)
	return sessions, err
}

func (d *StateDB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.backend.Close()
}

func (d *StateDB) putJSON(key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal state %s: %w", key, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.backend.Put(key, payload)
}

func (d *StateDB) getJSON(key string, target any) error {
	d.mu.Lock()
	payload, err := d.backend.Get(key)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode state %s: %w", key, err)
	}
	return nil
}
