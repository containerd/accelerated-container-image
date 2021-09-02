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

package cache

import (
	"math/rand"
	"runtime"
	"testing"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/rangesplit"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func testCacheGetOrRefillHelper(t *testing.T, config *Config) {
	t.Helper()
	Assert := assert.New(t)
	c := NewCachePool(config)
	for i := 0; i < 100; i++ {
		fileName := util.GetRandomString(10)
		fileContent := []byte(GetData())
		for j := 0; j < 10; j++ {
			for seg := range rangesplit.NewRangeSplit(0, 1024*1024, int64(len(fileContent)), int64(len(fileContent))).AllParts() {
				wg.Add(1)
				go func(offset int64, size int) {
					defer wg.Done()
					res, err := c.GetOrRefill(fileName, offset, size, func() ([]byte, error) {
						return fileContent[offset : offset+int64(size)], nil
					})
					if err != nil {
						log.Fatalf("GetOrRefill %s fail! %s", fileName, err)
					}
					if Assert.Equal(size, len(res)) {
						expected := fileContent[offset : offset+int64(size)]
						checkLen := util.Min(100, size)
						Assert.Equal(expected[:checkLen], res[:checkLen])
						Assert.Equal(expected[len(res)-checkLen:], res[len(res)-checkLen:])
					}
				}(seg.Index, seg.Count)
			}
		}
	}
	wg.Wait()
	runtime.GC()
}

func TestCacheGetOrRefill(t *testing.T) {
	testCacheGetOrRefillHelper(t, &Config{CacheSize: 1000 * 1024 * 1024, MaxEntry: 1, CacheMedia: media})
	testCacheGetOrRefillHelper(t, &Config{CacheSize: 100 * 1024 * 1024, MaxEntry: 1, CacheMedia: media})
	testCacheGetOrRefillHelper(t, &Config{CacheSize: 10 * 1024 * 1024, MaxEntry: 1, CacheMedia: media})
	testCacheGetOrRefillHelper(t, &Config{CacheSize: 1, MaxEntry: 1, CacheMedia: media})
}

func testCacheGetPutHostHelper(t *testing.T, config *Config) {
	t.Helper()
	Assert := assert.New(t)
	c := NewCachePool(config)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filename := util.GetRandomString(10)
			host := util.GetRandomString(1024)
			// get
			res, hit := c.GetHost(filename)
			Assert.Equal(false, hit)
			Assert.Equal("", res)
			// put
			hit = c.PutHost(filename, host)
			Assert.Equal(true, hit)
			// get
			res, hit = c.GetHost(filename)
			Assert.Equal(true, hit)
			Assert.Equal(host, res)
			// del
			c.DelHost(filename)
			// get
			res, hit = c.GetHost(filename)
			Assert.Equal(false, hit)
			Assert.Equal("", res)
		}()
	}
	wg.Wait()
}

func TestCacheGetPutHost(t *testing.T) {
	testCacheGetPutHostHelper(t, &Config{CacheSize: 1, MaxEntry: 1000 * 1024 * 1024, CacheMedia: media})
}

func testCacheGetPutLengthHelper(t *testing.T, config *Config) {
	t.Helper()
	Assert := assert.New(t)
	c := NewCachePool(config)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filename := util.GetRandomString(10)
			length := rand.Int63n(1024)
			// get
			res, hit := c.GetLen(filename)
			Assert.Equal(false, hit)
			Assert.Equal(int64(0), res)
			// put
			hit = c.PutLen(filename, length)
			Assert.Equal(true, hit)
			// get
			res, hit = c.GetLen(filename)
			Assert.Equal(true, hit)
			Assert.Equal(length, res)
		}()
	}
	wg.Wait()
}

func TestCacheGetPutLength(t *testing.T) {
	testCacheGetPutLengthHelper(t, &Config{CacheSize: 1, MaxEntry: 1000 * 1024 * 1024, CacheMedia: media})
}
