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

package util

import (
	"fmt"
	"math/rand"
	"net"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
)

// GetOutboundIP use detectAddr to get outbound IP
func GetOutboundIP(detectAddr string) net.IP {
	conn, err := net.Dial("udp", detectAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer func(conn net.Conn) {
		_ = conn.Close()
	}(conn)
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP
}

// GetRealPath get absolute path from relative
func GetRealPath(rawPath string) string {
	realPath, err := filepath.Abs(rawPath)
	if err != nil {
		log.Fatalf("Get real path fail! %s", err)
	}
	return realPath
}

// GetRandomString get random string
func GetRandomString(n int) string {
	randBytes := make([]byte, n/2)
	rand.Read(randBytes)
	return fmt.Sprintf("%x", randBytes)
}

// CheckTCPConn will connect to address to check is connect
func CheckTCPConn(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil || conn == nil {
		log.Infof("Connect to %s failed! %s", address, err)
		return false
	}
	_ = conn.Close()
	return true
}

func Max64(x, y int64) int64 {
	if x > y {
		return x
	}
	return y
}

func Min64(x, y int64) int64 {
	if x < y {
		return x
	}
	return y
}

func Min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func Max(x, y int) int {
	if x > y {
		return x
	}
	return y
}
