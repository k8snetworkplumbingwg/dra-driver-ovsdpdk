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

package devicestate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("upsertDeviceStatus", func() {
	entry := func(driver, pool, device string, shareID *string) resourceapi.AllocatedDeviceStatus {
		return resourceapi.AllocatedDeviceStatus{
			Driver:  driver,
			Pool:    pool,
			Device:  device,
			ShareID: shareID,
		}
	}

	It("appends to an empty list", func() {
		e := entry("driver-a", "pool-0", "dev-0", nil)
		result := upsertDeviceStatus(nil, e)
		Expect(result).To(HaveLen(1))
		Expect(result[0]).To(Equal(e))
	})

	It("appends when no entry matches the key", func() {
		existing := []resourceapi.AllocatedDeviceStatus{
			entry("driver-a", "pool-0", "dev-0", nil),
		}
		e := entry("driver-a", "pool-0", "dev-1", nil)
		result := upsertDeviceStatus(existing, e)
		Expect(result).To(HaveLen(2))
		Expect(result[1]).To(Equal(e))
	})

	It("replaces an existing entry with a nil ShareID", func() {
		old := entry("driver-a", "pool-0", "dev-0", nil)
		old.Data = nil // distinguishable from the new entry
		existing := []resourceapi.AllocatedDeviceStatus{old}

		updated := entry("driver-a", "pool-0", "dev-0", nil)
		result := upsertDeviceStatus(existing, updated)
		Expect(result).To(HaveLen(1))
		Expect(result[0]).To(Equal(updated))
	})

	It("replaces an existing entry with a matching non-nil ShareID", func() {
		sid := ptr.To("share-1")
		old := entry("driver-a", "pool-0", "dev-0", sid)
		existing := []resourceapi.AllocatedDeviceStatus{old}

		updated := entry("driver-a", "pool-0", "dev-0", ptr.To("share-1"))
		result := upsertDeviceStatus(existing, updated)
		Expect(result).To(HaveLen(1))
		Expect(result[0]).To(Equal(updated))
	})

	It("appends rather than replacing when ShareIDs differ", func() {
		e1 := entry("driver-a", "pool-0", "dev-0", ptr.To("share-1"))
		existing := []resourceapi.AllocatedDeviceStatus{e1}

		e2 := entry("driver-a", "pool-0", "dev-0", ptr.To("share-2"))
		result := upsertDeviceStatus(existing, e2)
		Expect(result).To(HaveLen(2), "different ShareIDs must produce distinct entries")
		Expect(result[0]).To(Equal(e1))
		Expect(result[1]).To(Equal(e2))
	})

	It("appends rather than replacing when one ShareID is nil and the other is not", func() {
		e1 := entry("driver-a", "pool-0", "dev-0", nil)
		existing := []resourceapi.AllocatedDeviceStatus{e1}

		e2 := entry("driver-a", "pool-0", "dev-0", ptr.To("share-1"))
		result := upsertDeviceStatus(existing, e2)
		Expect(result).To(HaveLen(2), "nil vs non-nil ShareID must produce distinct entries")
	})
})
