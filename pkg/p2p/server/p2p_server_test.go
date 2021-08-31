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

package server

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func testP2PGet(t *testing.T, transport *http.Transport, reqURL string) {
	t.Helper()
	Assert := assert.New(t)
	client := &http.Client{Transport: transport}
	res, err := client.Get(reqURL)
	if err != nil {
		log.Fatalf("Proxy client get %s fail! %s", reqURL, err)
	}
	parseURL, err := url.Parse(reqURL)
	if err != nil {
		log.Fatalf("Parser url %s fail! %s", reqURL, err)
	}
	proxyBody, _ := ioutil.ReadAll(res.Body)
	path := parseURL.Path[31:]
	if strings.Contains(parseURL.Path, "https") {
		path = path[1:]
	}
	r, hit := requestMap.Load(path)
	if !hit {
		log.Fatal("Fatal!")
	}
	body := []byte(r.(string))
	if Assert.Equal(len(body), len(proxyBody)) {
		checkLen := util.Min(100, len(body))
		Assert.Equal(body[:checkLen], proxyBody[:checkLen])
		Assert.Equal(body[len(body)-checkLen:], proxyBody[len(body)-checkLen:])
	}
}

func testP2PServerHelper(t *testing.T, serverList, httpServerList, httpsServerList []*http.Server, serveBySSL bool) {
	t.Helper()
	transport := &http.Transport{
		MaxConnsPerHost: 100,
	}
	for i := 0; i < 10; i++ {
		filename := util.GetRandomString(64)
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func(filename string) {
				defer wg.Done()
				p2pAddress := serverList[rand.Intn(len(serverList))].Addr
				httpAddress := httpServerList[rand.Intn(len(httpServerList))].Addr
				httpsAddress := httpsServerList[rand.Intn(len(httpsServerList))].Addr
				var originURL string
				if rand.Int()%2 == 0 {
					originURL = fmt.Sprintf("https://127.0.0.1%s/v2/blobs/sha256/%s/data", httpsAddress, filename)
				} else {
					originURL = fmt.Sprintf("http://127.0.0.1%s/v2/blobs/sha256/%s/data", httpAddress, filename)
				}
				if serveBySSL {
					testP2PGet(t, transport, fmt.Sprintf("https://127.0.0.1%s/dadip2p/%s", p2pAddress, originURL))
				} else {
					testP2PGet(t, transport, fmt.Sprintf("http://127.0.0.1%s/dadip2p/%s", p2pAddress, originURL))
				}
			}(filename)
		}
	}
	wg.Wait()
}

type TestMatrix struct {
	root, agent int
	serveBySSL  bool
}

func TestP2PServer(t *testing.T) {
	testMatrix := []TestMatrix{
		{1, 3, false},
		{1, 3, true},
	}
	httpServerList := StartHTTPServers()
	httpsServerList := StartHTTPSServers()
	for _, v := range testMatrix {
		// prepare
		serverList := StartServers(v.root, v.agent, v.serveBySSL, false)
		wg.Wait()
		// test
		testP2PServerHelper(t, serverList, httpServerList, httpsServerList, v.serveBySSL)
		// clean
		for _, server := range serverList {
			_ = server.Shutdown(context.TODO())
		}
		log.Info("Server shutdown success!")
		runtime.GC()
	}
	for _, server := range httpServerList {
		_ = server.Shutdown(context.TODO())
	}
	for _, server := range httpsServerList {
		_ = server.Shutdown(context.TODO())
	}
}
