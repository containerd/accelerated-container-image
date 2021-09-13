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
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	log "github.com/sirupsen/logrus"
)

const media = "/tmp/p2p-cache"

var wg sync.WaitGroup

func setup() {
	rand.Seed(time.Now().UnixNano())
	log.SetFormatter(&log.TextFormatter{ForceColors: true})
	log.SetLevel(log.DebugLevel)
	// ignore test.root
	flag.Bool("test.root", false, "")
}

func teardown() {
	if err := os.RemoveAll(media); err != nil {
		fmt.Printf("Remove %s failed! %s", media, err)
	}
}

func TestMain(m *testing.M) {
	log.Info("Start test!")
	setup()
	code := m.Run()
	teardown()
	os.Exit(code)
}

func GetData() string {
	const blockSize = 1024 * 1024
	length := rand.Intn(4) * blockSize
	length += rand.Intn(blockSize)
	return util.GetRandomString(length)
}
