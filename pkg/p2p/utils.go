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
	"fmt"
	"math/rand"
	"net"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
)

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

func GetRealPath(rawPath string) string {
	realPath, _ := filepath.Abs(rawPath)
	return realPath
}

func GetRandomString(n int) string {
	randBytes := make([]byte, n/2)
	rand.Read(randBytes)
	return fmt.Sprintf("%x", randBytes)
}

func IsContain(arr []string, item string) bool {
	for _, v := range arr {
		if v == item {
			return true
		}
	}
	return false
}

func CheckTCPConn(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil || conn == nil {
		log.Infof("Connect to %s failed! %s", address, err)
		return false
	}
	_ = conn.Close()
	return true
}
