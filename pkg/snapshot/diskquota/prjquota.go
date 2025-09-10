//go:build linux
// +build linux

/*
   Copyright The Accelerated Container Image Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package diskquota

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/log"
	"github.com/docker/go-units"
)

const (
	// QuotaMinID represents the minimum quota id.
	// The value is unit32(2^24).
	QuotaMinID = uint32(16777216)

	// QuotaMaxID represents the maximum quota id.
	QuotaMaxID = uint32(200000000)
)

func safeConvertToUInt32(strVal string) (uint32, error) {
	intVal, err := strconv.Atoi(strVal)
	if err != nil {
		return 0, fmt.Errorf("failed to parse integer: %w", err)
	}

	// Check if the value is within the uint32 range and is non-negative.
	if intVal < 0 || intVal > math.MaxUint32 {
		return 0, fmt.Errorf("value %d is out of range for uint32", intVal)
	}

	return uint32(intVal), nil
}

// SetDiskQuotaBytes set dir project quota to the quotaId
func SetDiskQuotaBytes(dir string, limit int64, quotaID uint32) error {
	driver := &PrjQuotaDriver{}
	mountPoint, hasQuota, err := driver.CheckMountpoint(dir)
	if err != nil {
		return err
	}
	if !hasQuota {
		// no need to remount option prjquota for mountpoint
		return fmt.Errorf("mountpoint: (%s) not enable prjquota", mountPoint)
	}

	if err := checkDevLimit(mountPoint, uint64(limit)); err != nil {
		return fmt.Errorf("failed to check device limit, dir: (%s), limit: (%d)kb: %w", dir, limit, err)
	}

	err = driver.SetFileAttr(dir, quotaID)
	if err != nil {
		return fmt.Errorf("failed to set subtree, dir: (%s), quota id: (%d): %w", dir, quotaID, err)
	}

	return driver.setQuota(quotaID, uint64(limit/1024), mountPoint)
}

// PrjQuotaDriver represents project quota driver.
type PrjQuotaDriver struct {
	lock sync.Mutex

	// quotaIDs saves all of quota ids.
	// key: quota ID which means this ID is used in the global scope.
	// value: stuct{}
	QuotaIDs map[uint32]struct{}

	// lastID is used to mark last used quota ID.
	// quota ID is allocated increasingly by sequence one by one.
	LastID uint32
}

// SetDiskQuota uses the following two parameters to set disk quota for a directory.
// * quota size: a byte size of requested quota.
// * quota ID: an ID represent quota attr which is used in the global scope.
func (quota *PrjQuotaDriver) SetDiskQuota(dir string, size string, quotaID uint32) error {
	mountPoint, hasQuota, err := quota.CheckMountpoint(dir)
	if err != nil {
		return err
	}
	if !hasQuota {
		// no need to remount option prjquota for mountpoint
		return fmt.Errorf("mountpoint: (%s) not enable prjquota", mountPoint)
	}

	limit, err := units.RAMInBytes(size)
	if err != nil {
		return fmt.Errorf("failed to change size: (%s) to kilobytes: %w", size, err)
	}

	if err := checkDevLimit(mountPoint, uint64(limit)); err != nil {
		return fmt.Errorf("failed to check device limit, dir: (%s), limit: (%d)kb: %w", dir, limit, err)
	}

	err = quota.SetFileAttr(dir, quotaID)
	if err != nil {
		return fmt.Errorf("failed to set subtree, dir: (%s), quota id: (%d): %w", dir, quotaID, err)
	}

	return quota.setQuota(quotaID, uint64(limit/1024), mountPoint)
}

func (quota *PrjQuotaDriver) CheckMountpoint(dir string) (string, bool, error) {
	mountInfo, err := mount.Lookup(dir)
	if err != nil {
		return "", false, fmt.Errorf("failed to get mount info, dir(%s): %w", dir, err)
	}
	if strings.Contains(mountInfo.VFSOptions, "prjquota") {
		return mountInfo.Mountpoint, true, nil
	}
	return mountInfo.Mountpoint, false, nil
}

// setQuota uses system tool "setquota" to set project quota for binding of limit and mountpoint and quotaID.
// * quotaID: quota ID which means this ID is used in the global scope.
// * blockLimit: block limit number for mountpoint.
// * mountPoint: the mountpoint of the device in the filesystem
// ext4: setquota -P qid $softlimit $hardlimit $softinode $hardinode mountpoint
func (quota *PrjQuotaDriver) setQuota(quotaID uint32, blockLimit uint64, mountPoint string) error {
	quotaIDStr := strconv.FormatUint(uint64(quotaID), 10)
	blockLimitStr := strconv.FormatUint(blockLimit, 10)

	// ext4 set project quota limit
	// log.L.Infof("setquota -P %s 0 %s 0 0 %s", quotaIDStr, blockLimitStr, mountPoint)
	stdout, stderr, err := ExecSync("setquota", "-P", quotaIDStr, "0", blockLimitStr, "0", "0", mountPoint)
	if err != nil {
		return fmt.Errorf("failed to set quota, mountpoint: (%s), quota id: (%d), quota: (%d kbytes), stdout: (%s), stderr: (%s): %w",
			mountPoint, quotaID, blockLimit, stdout, stderr, err)
	}
	return nil
}

// GetQuotaIDInFileAttr gets attributes of the file which is in the inode.
// The returned result is quota ID.
// return 0 if failure happens, since quota ID must be positive.
// execution command: `lsattr -p $dir`
func (quota *PrjQuotaDriver) GetQuotaIDInFileAttr(dir string) uint32 {
	parent := path.Dir(dir)

	stdout, _, err := ExecSync("lsattr", "-p", parent)
	if err != nil {
		// failure, then return invalid value 0 for quota ID.
		return 0
	}

	// example output:
	// 16777256 --------------e---P ./exampleDir
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		parts := strings.Split(line, " ")
		if len(parts) > 2 && parts[2] == dir {
			// find the corresponding quota ID, return directly.
			qid, _ := safeConvertToUInt32(parts[0])
			return qid
		}
	}

	return 0
}

// GetNextQuotaID returns the next available quota id.
func (quota *PrjQuotaDriver) GetNextQuotaID() (quotaID uint32, err error) {
	quota.lock.Lock()
	defer quota.lock.Unlock()

	if quota.LastID == 0 {
		quota.QuotaIDs, quota.LastID, err = loadQuotaIDs("-Pan")
		if err != nil {
			return 0, fmt.Errorf("failed to load quota list: %w", err)
		}
	}
	id := quota.LastID
	for {
		if id < QuotaMinID {
			id = QuotaMinID
		}
		id++
		if _, ok := quota.QuotaIDs[id]; !ok {
			if id <= QuotaMaxID {
				break
			}
			log.L.Infof("reach the maximum, try to reuse quotaID")
			quota.QuotaIDs, quota.LastID, err = loadQuotaIDs("-Pan")
			if err != nil {
				return 0, fmt.Errorf("failed to load quota list: %w", err)
			}
			id = quota.LastID
		}
	}
	quota.QuotaIDs[id] = struct{}{}
	quota.LastID = id

	return id, nil
}

// SetFileAttr set the file attr.
// ext4: chattr -p quotaid +P $DIR
func (quota *PrjQuotaDriver) SetFileAttr(dir string, quotaID uint32) error {
	strID := strconv.FormatUint(uint64(quotaID), 10)

	// ext4 use chattr to change project id
	stdout, stderr, err := ExecSync("chattr", "-p", strID, "+P", dir)
	if err != nil {
		return fmt.Errorf("failed to set file(%s) quota id(%s), stdout: (%s), stderr: (%s): %w", dir, strID, stdout, stderr, err)
	}
	log.L.Debugf("set quota id (%s) to file (%s) attr", strID, dir)

	return nil
}
