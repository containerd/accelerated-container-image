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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func isContain(arr []string, item string) bool {
	for _, v := range arr {
		if v == item {
			return true
		}
	}
	return false
}

func checkTCPConn(t *testing.T, address string) bool {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil || conn == nil {
		t.Logf("Connect to %s failed! %s", address, err)
		return false
	}
	_ = conn.Close()
	return true
}

func startServer(t *testing.T, runMode string, rootList []string, port int, serveBySSL, proxyHTTPS bool) {
	var config = DeployConfig{}
	config.LogLevel = "debug"
	config.RunMode = runMode
	config.RootList = rootList
	config.NodeIP = "127.0.0.1"
	config.DetectAddr = ""
	config.ServeBySSL = serveBySSL
	config.Port = port
	config.CertConfig.CertEnable = proxyHTTPS
	config.CertConfig.GenerateCert = true
	config.CertConfig.CertPath = "p2pcert.pem"
	config.CertConfig.KeyPath = "p2pcert.key"
	config.CacheConfig.MemCacheEnable = true
	config.CacheConfig.MemCacheSize = 100 * 1024 * 1024
	config.CacheConfig.FileCacheEnable = true
	config.CacheConfig.FileCacheSize = 1000 * 1024 * 1024
	config.CacheConfig.FileCachePath = "/tmp/cache"
	config.PrefetchConfig.PrefetchEnable = true
	config.PrefetchConfig.PrefetchThread = 64
	go func() {
		cache := NewCachePool(&CacheConfig{
			MaxEntry:   config.CacheConfig.MemCacheSize,
			CacheSize:  config.CacheConfig.FileCacheSize,
			CacheMedia: config.CacheConfig.FileCachePath,
		})
		hp := NewHostPicker(config.RootList, cache)
		fs := NewP2PFS(&FSConfig{
			CachePool:       cache,
			HostPicker:      hp,
			APIKey:          "dadip2p",
			PrefetchWorkers: config.PrefetchConfig.PrefetchThread,
		})
		var myAddr string
		if !config.ServeBySSL {
			myAddr = fmt.Sprintf("http://%s:%d", config.NodeIP, config.Port)
		} else {
			myAddr = fmt.Sprintf("https://%s:%d", config.NodeIP, config.Port)
		}
		serverHandler := NewP2PServer(&ServerConfig{
			MyAddr: myAddr,
			Fs:     fs,
			APIKey: "dadip2p",
		})
		var cert []tls.Certificate
		if config.CertConfig.CertEnable {
			cert = []tls.Certificate{*GetRootCA(config.CertConfig.CertPath, config.CertConfig.KeyPath, config.CertConfig.GenerateCert)}
		}
		server := &http.Server{
			Addr:      fmt.Sprintf(":%d", config.Port),
			Handler:   serverHandler,
			TLSConfig: &tls.Config{Certificates: cert},
		}
		if err := server.ListenAndServe(); err != nil {
			t.Errorf("Server %s on port %d exit! %s", runMode, port, err)
		}
	}()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for !checkTCPConn(t, address) {
		t.Logf("check address %s failed! retry...", address)
		time.Sleep(time.Second)
	}
	t.Logf("Start %s on port %d success!", runMode, port)
}

func startServers(t *testing.T, serveBySSL, proxyHTTPS bool) []string {
	startServer(t, "root", make([]string, 0), 19141, serveBySSL, proxyHTTPS)
	startServer(t, "root", make([]string, 0), 19142, serveBySSL, proxyHTTPS)
	startServer(t, "agent", []string{"127.0.0.1:19141", "127.0.0.1:19142"}, 19151, serveBySSL, proxyHTTPS)
	startServer(t, "agent", []string{"127.0.0.1:19141", "127.0.0.1:19142"}, 19152, serveBySSL, proxyHTTPS)
	startServer(t, "agent", []string{"127.0.0.1:19141", "127.0.0.1:19142"}, 19153, serveBySSL, proxyHTTPS)
	return []string{
		"127.0.0.1:19141",
		"127.0.0.1:19142",
		"127.0.0.1:19151",
		"127.0.0.1:19152",
		"127.0.0.1:19153",
	}
}

func testGet(t *testing.T, c *http.Client, url string) {
	t.Helper()
	Assert := assert.New(t)
	proxyRes, err := c.Get(url)
	if err != nil {
		t.Errorf("Proxy client get %s failed! %s", url, err)
	}
	res, err := http.Get(url)
	if err != nil {
		t.Errorf("HTTP client get %s failed! %s", url, err)
	}
	// check body
	proxyBody, _ := ioutil.ReadAll(proxyRes.Body)
	body, err := ioutil.ReadAll(res.Body)
	if Assert.Equal(len(proxyBody), len(body)) {
		Assert.Equal(body, proxyBody)
	}
	// check header
	checkList := []string{
		"set-cookie",
		"content-type",
	}
	for k, v := range res.Header {
		if isContain(checkList, k) {
			Assert.Equal(v, proxyRes.Header.Values(k))
		}
	}
}

func testProxy(t *testing.T, proxyAddress string, proxyHTTPS bool) {
	proxyURL, err := url.Parse(fmt.Sprintf("http://%s", proxyAddress))
	if err != nil {
		t.Fatalf("Proxy url parse failed! %s", err)
	}
	var client *http.Client
	if !proxyHTTPS {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
	} else {
		pool := x509.NewCertPool()
		CACert, err := ioutil.ReadFile("p2pcert.pem")
		if err != nil {
			t.Fatalf("Read CA cert failed! %s", err)
		}
		pool.AppendCertsFromPEM(CACert)
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					RootCAs: pool,
				},
			},
		}
	}
	// test transport url
	//testGet(t, client, "http://1616.net")
	//if proxyHTTPS {
	//	testGet(t, client, "https://www.baidu.com")
	//}
	// test p2p url
	//testGet(t, client, "http://repo-2.cloud-iaas.nec.com/repo/pub/docker/registry/v2/blobs/sha256/0b/0b9bfabab7c119abe303f22a146ff78be4ab0abdc798b0a0e97e94e80238a7e8/data")
	if proxyHTTPS {
		testGet(t, client, "https://repo-2.cloud-iaas.nec.com/repo/pub/docker/registry/v2/blobs/sha256/0b/0b9bfabab7c119abe303f22a146ff78be4ab0abdc798b0a0e97e94e80238a7e8/data")
	}
}

func TestE2E_1(t *testing.T) {
	const serveBySSL = false
	const proxyHTTPS = false
	for _, server := range startServers(t, serveBySSL, proxyHTTPS) {
		testProxy(t, server, proxyHTTPS)
	}
}

func TestE2E_2(t *testing.T) {
	const serveBySSL = false
	const proxyHTTPS = true
	for _, server := range startServers(t, serveBySSL, proxyHTTPS) {
		testProxy(t, server, proxyHTTPS)
	}
}
