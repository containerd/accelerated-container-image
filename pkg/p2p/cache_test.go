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

package p2p

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

var wg sync.WaitGroup

const media = "/tmp/cache"
const fileNum = 100
const segNum = 50

func init() {
	// ignore test.root
	flag.Bool("test.root", false, "")
}

func getRandomString(n int) string {
	randBytes := make([]byte, n/2)
	rand.Read(randBytes)
	return fmt.Sprintf("%x", randBytes)
}

func setup() {
	rand.Seed(time.Now().UnixNano())
}

func teardown() {
	if err := os.RemoveAll(media); err != nil {
		fmt.Printf("Remove %s failed! %s", media, err)
	}
}

func testCacheGetOrRefillHelper(t *testing.T, config *CacheConfig) {
	t.Helper()
	Assert := assert.New(t)
	c := NewCachePool(config)
	fileName := make([]string, fileNum)
	fileContent := make([][]byte, fileNum)
	for i := 0; i < fileNum; i++ {
		fileName[i] = getRandomString(10)
		fileContent[i] = []byte(getRandomString(1024 * 1024))
	}
	for i := 0; i < fileNum; i++ {
		for j := 0; j < segNum; j++ {
			wg.Add(1)
			go func(i, j int) {
				defer wg.Done()
				isError := 0
				if rand.Int()%5 == 0 {
					isError = 1
				}
				offset := rand.Intn(len(fileContent[i]) / 2)
				size := rand.Intn(len(fileContent[i]) - offset)
				res, err := c.GetOrRefill(fileName[i], int64(offset), size, func() ([]byte, error) {
					if isError == 1 {
						isError = 2
						return nil, errors.New("creator failed")
					}
					return fileContent[i][offset : offset+size], nil
				})
				if isError == 2 {
					Assert.NotEqual(nil, err)
					Assert.Equal([]byte(nil), res)
				} else {
					if Assert.Equal(nil, err) {
						Assert.Equal(fileContent[i][offset:offset+size], res)
					} else {
						t.Log(err)
					}
				}
			}(i, j)
		}
	}
	wg.Wait()
	runtime.GC()
}

func TestCacheGetOrRefill(t *testing.T) {
	testCacheGetOrRefillHelper(t, &CacheConfig{CacheSize: 100 * 1024 * 1024, MaxEntry: 0, CacheMedia: media})
	testCacheGetOrRefillHelper(t, &CacheConfig{CacheSize: 10 * 1024 * 1024, MaxEntry: 0, CacheMedia: media})
	testCacheGetOrRefillHelper(t, &CacheConfig{CacheSize: 1 * 1024 * 1024, MaxEntry: 0, CacheMedia: media})
	testCacheGetOrRefillHelper(t, &CacheConfig{CacheSize: 0.5 * 1024 * 1024, MaxEntry: 0, CacheMedia: media})
}

func TestCacheSegment(t *testing.T) {
	Assert := assert.New(t)
	c := NewCachePool(&CacheConfig{CacheSize: 100 * 1024 * 1024, MaxEntry: 100 * 1024 * 1024, CacheMedia: media})
	filename := getRandomString(10)
	content := []byte(getRandomString(1024 * 1024))
	{
		// [100,400]
		res, err := c.GetOrRefill(filename, 100, 300, func() ([]byte, error) {
			return content[100:400], nil
		})
		Assert.Equal(nil, err)
		Assert.Equal(content[100:400], res)
	}
	{
		// [100,200]
		res, err := c.GetOrRefill(filename, 100, 100, func() ([]byte, error) {
			return make([]byte, 100), nil
		})
		Assert.Equal(nil, err)
		Assert.Equal(content[100:200], res)
	}
	{
		// TODO support cache
		// [200,300]
		//res, err := c.GetOrRefill(filename, 200, 100, func() ([]byte, error) {
		//	return make([]byte, 100), nil
		//})
		//Assert.Equal(nil, err)
		//Assert.Equal(content[200:300], res)
	}
	{
		// TODO support cache
		// [300,400]
		//res, err := c.GetOrRefill(filename, 300, 100, func() ([]byte, error) {
		//	return make([]byte, 100), nil
		//})
		//Assert.Equal(nil, err)
		//Assert.Equal(content[300:400], res)
	}
	{
		// [50,150]
		res, err := c.GetOrRefill(filename, 50, 100, func() ([]byte, error) {
			return content[50:150], nil
		})
		Assert.Equal(nil, err)
		Assert.Equal(content[50:150], res)
	}
	{
		// [350,450]
		res, err := c.GetOrRefill(filename, 350, 100, func() ([]byte, error) {
			return content[350:450], nil
		})
		Assert.Equal(nil, err)
		Assert.Equal(content[350:450], res)
	}
}

func TestCacheGetPutHost(t *testing.T) {
	Assert := assert.New(t)
	c := NewCachePool(&CacheConfig{CacheSize: 100 * 1024 * 1024, MaxEntry: 100 * 1024 * 1024, CacheMedia: media})
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filename := getRandomString(10)
			host := getRandomString(1000)
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

func TestCacheGetPutLength(t *testing.T) {
	Assert := assert.New(t)
	c := NewCachePool(&CacheConfig{CacheSize: 100 * 1024 * 1024, MaxEntry: 100 * 1024 * 1024, CacheMedia: media})
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filename := getRandomString(10)
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

func TestMain(m *testing.M) {
	setup()
	code := m.Run()
	teardown()
	os.Exit(code)
}
