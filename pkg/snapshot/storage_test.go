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

package snapshot

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestWaitForBlockDevice(t *testing.T) {
	t.Run("waits until the device is ready", func(t *testing.T) {
		calls := 0
		// The fake replaces sysfs and /dev access, so the test never touches
		// a real block device and can run unprivileged on any host.
		find := func(deviceNumber string) (string, error) {
			calls++
			if deviceNumber != "0:0:0:1" {
				t.Fatalf("find() got device number %q, want 0:0:0:1", deviceNumber)
			}
			if calls < 3 {
				return "", errors.New("not ready")
			}
			return "/dev/fakeblk0", nil
		}

		got, err := waitForBlockDevice("0:0:0:1", 5, 0, find)
		if err != nil {
			t.Fatalf("waitForBlockDevice() error: %v", err)
		}
		if got != "/dev/fakeblk0" {
			t.Fatalf("waitForBlockDevice() = %q, want /dev/fakeblk0", got)
		}
		if calls != 3 {
			t.Fatalf("find called %d times, want 3", calls)
		}
	})

	t.Run("returns the last error when the device never becomes ready", func(t *testing.T) {
		calls := 0
		find := func(string) (string, error) {
			calls++
			return "", fmt.Errorf("attempt %d: not ready", calls)
		}

		_, err := waitForBlockDevice("0:0:0:1", 2, 0, find)
		if err == nil {
			t.Fatal("waitForBlockDevice() = nil error, want timeout")
		}
		if !strings.Contains(err.Error(), "not ready after 2 attempts") {
			t.Fatalf("waitForBlockDevice() error = %v, want a timeout", err)
		}
		if !strings.Contains(err.Error(), "attempt 2") {
			t.Fatalf("waitForBlockDevice() error = %v, want the last error", err)
		}
		if calls != 2 {
			t.Fatalf("find called %d times, want 2", calls)
		}
	})
}
