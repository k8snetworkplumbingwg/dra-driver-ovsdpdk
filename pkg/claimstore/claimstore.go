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

// Package claimstore persists prepared resource-claim state between Prepare
// and Unprepare calls in the driver layer.
package claimstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
	k8stypes "k8s.io/apimachinery/pkg/types"
	klog "k8s.io/klog/v2"

	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

// preparedClaimsBucket is the name of the main bbolt bucket.
var preparedClaimsBucket = []byte("prepared_claims")

// preparedClaimStore is a bbolt-based thread-safe store of PreparedDevice
// records keyed by claim UID.
type preparedClaimStore struct {
	db *bbolt.DB
}

// New creates a new PreparedClaimStore.
func New(dbPath string) (PreparedClaimStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt database: %w", err)
	}

	if err := db.Update(func(tx *bbolt.Tx) error {
		// Ensure the bucket exists.
		b, err := tx.CreateBucketIfNotExists(preparedClaimsBucket)
		if err != nil {
			return err
		}

		// If the bucket existed and we have restored claims, log it.
		if b.Stats().KeyN > 0 {
			dump := make(map[k8stypes.UID][]*dratypes.PreparedDevice)

			klog.Infof("Restored %d prepared claims from checkpoint", b.Stats().KeyN)

			err = b.ForEach(func(k, v []byte) error {
				var devices []*dratypes.PreparedDevice
				if err := json.Unmarshal(v, &devices); err != nil {
					return fmt.Errorf("unmarshal claim %s: %w", string(k), err)
				}
				dump[k8stypes.UID(string(k))] = devices
				return nil
			})
			klog.V(2).Infof("Restored devices: %v", dump)
		}
		return err
	}); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			klog.Errorf("Failed to close bucket after failed update: %v", closeErr)
		}
		return nil, fmt.Errorf("create bucket: %w", err)
	}

	return &preparedClaimStore{
		db: db,
	}, nil
}

// Get returns the PreparedDevices for the given claim UID.
// Returns nil slice if not found.
func (s *preparedClaimStore) Get(claimUID k8stypes.UID) ([]*dratypes.PreparedDevice, error) {
	var result []*dratypes.PreparedDevice

	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(preparedClaimsBucket)
		if b == nil {
			return fmt.Errorf("no bucket")
		}

		v := b.Get([]byte(string(claimUID)))
		if v != nil {
			return json.Unmarshal(v, &result)
		}
		return nil
	})

	return result, err
}

// Set stores the PreparedDevices for the given claim UID.
func (s *preparedClaimStore) Set(claimUID k8stypes.UID, devices []*dratypes.PreparedDevice) error {
	data, err := json.Marshal(devices)
	if err != nil {
		return fmt.Errorf("marshal claim %s: %w", claimUID, err)
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(preparedClaimsBucket)
		if b == nil {
			return fmt.Errorf("no bucket")
		}
		return b.Put([]byte(string(claimUID)), data)
	})
}

// Delete removes the PreparedDevices for the given claim UID.
func (s *preparedClaimStore) Delete(claimUID k8stypes.UID) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(preparedClaimsBucket)
		if b == nil {
			return fmt.Errorf("no bucket")
		}
		return b.Delete([]byte(string(claimUID)))
	})
}

// Close closes the underlying database.
func (s *preparedClaimStore) Close() error {
	return s.db.Close()
}
