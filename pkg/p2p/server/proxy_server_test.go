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
	"math/rand"
	"net/http"
	"net/url"
	"runtime"
	"testing"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func testProxyGet(t *testing.T, transport *http.Transport, url string, isRedirect bool) {
	t.Helper()
	Assert := assert.New(t)
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		log.Fatalf("Client get url %s fail! %s", url, err)
	}
	if isRedirect {
		Assert.Equal(307, resp.StatusCode)
	} else {
		Assert.Equal(200, resp.StatusCode)
	}
}

func testProxyServerHelper(t *testing.T, port int, proxyHTTPS bool) {
	t.Helper()
	proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("Proxy url parse fail! %s", err)
	}
	transport := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		MaxConnsPerHost: 100,
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filename := util.GetRandomString(64)
			if rand.Int()%2 == 0 {
				if rand.Int()%2 == 0 {
					testProxyGet(t, transport, fmt.Sprintf("https://127.0.0.1:%d/%s", 19132, filename), false)
				} else {
					testProxyGet(t, transport, fmt.Sprintf("http://127.0.0.1:%d/%s", 19131, filename), false)
				}
			} else {
				// redirect
				if rand.Int()%2 == 0 {
					if proxyHTTPS {
						testProxyGet(t, transport, fmt.Sprintf("https://127.0.0.1:%d/v2/blobs/sha256/%s/data", 19132, filename), true)
					} else {
						testProxyGet(t, transport, fmt.Sprintf("https://127.0.0.1:%d/v2/blobs/sha256/%s/data", 19132, filename), false)
					}
				} else {
					testProxyGet(t, transport, fmt.Sprintf("http://127.0.0.1:%d/v2/blobs/sha256/%s/data", 19131, filename), true)
				}
			}
		}()
	}
	wg.Wait()
}

type TestProxyMatrix struct {
	proxyHTTPS bool
}

func TestProxyServer(t *testing.T) {
	testProxyMatrix := []TestProxyMatrix{
		{false},
		{true},
	}
	httpServers := StartHTTPServers()
	httpsServers := StartHTTPSServers()
	port := 19145
	for _, v := range testProxyMatrix {
		// prepare
		server := StartServer(0, "root", nil, port, false, v.proxyHTTPS)
		wg.Wait()
		// test
		testProxyServerHelper(t, port+100, v.proxyHTTPS)
		port++
		// clean
		_ = server.Shutdown(context.TODO())
		log.Info("Server shutdown success!")
		runtime.GC()
	}
	for _, server := range httpServers {
		_ = server.Shutdown(context.TODO())
	}
	for _, server := range httpsServers {
		_ = server.Shutdown(context.TODO())
	}
}
