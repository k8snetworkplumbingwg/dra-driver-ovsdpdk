/*
 * Copyright 2026 Red Hat, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package podmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
	k8stypes "k8s.io/apimachinery/pkg/types"
	klog "k8s.io/klog/v2"

	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

var preparedClaimsBucket = []byte("prepared_claims")

// boltCheckpoint implements the Checkpoint interface using bbolt for
// persistent storage of prepared claim state.
type boltCheckpoint struct {
	db *bolt.DB
}

// NewBoltCheckpoint creates a new bbolt-backed Checkpoint.
func NewBoltCheckpoint(dbPath string) (Checkpoint, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt database: %w", err)
	}

	// Ensure the bucket exists.
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(preparedClaimsBucket)
		return err
	}); err != nil {
		if close_err := db.Close(); close_err != nil {
			klog.Errorf("Failed to close bucket after failed update: %v", close_err)
		}
		return nil, fmt.Errorf("create bucket: %w", err)
	}

	return &boltCheckpoint{db: db}, nil
}

// Load loads all persisted claim entries from the database.
func (c *boltCheckpoint) Load() (map[k8stypes.UID][]*dratypes.PreparedDevice, error) {
	result := make(map[k8stypes.UID][]*dratypes.PreparedDevice)

	err := c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(preparedClaimsBucket)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var devices []*dratypes.PreparedDevice
			if err := json.Unmarshal(v, &devices); err != nil {
				return fmt.Errorf("unmarshal claim %s: %w", string(k), err)
			}
			result[k8stypes.UID(string(k))] = devices
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// Store persists the prepared devices for a single claim.
func (c *boltCheckpoint) Store(claimUID k8stypes.UID, devices []*dratypes.PreparedDevice) error {
	data, err := json.Marshal(devices)
	if err != nil {
		return fmt.Errorf("marshal claim %s: %w", claimUID, err)
	}

	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(preparedClaimsBucket)
		return b.Put([]byte(string(claimUID)), data)
	})
}

// Delete removes a claim's entry from the persistent store.
func (c *boltCheckpoint) Delete(claimUID k8stypes.UID) error {
	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(preparedClaimsBucket)
		return b.Delete([]byte(string(claimUID)))
	})
}

// Close closes the bolt database.
func (c *boltCheckpoint) Close() error {
	return c.db.Close()
}
