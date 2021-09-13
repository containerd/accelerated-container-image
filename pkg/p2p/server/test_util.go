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
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/certificate"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/configure"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	log "github.com/sirupsen/logrus"
)

const Media = "/tmp/cache"

var (
	wg         sync.WaitGroup
	requestMap = &sync.Map{}
)

func StartServer(idx int, runMode string, rootList []string, port int, serveBySSL, proxyHTTPS bool) *http.Server {
	var config = &configure.DeployConfig{}
	config.LogLevel = "debug"
	config.APIKey = "dadip2p"
	// proxy
	config.ProxyConfig.Port = port + 100
	config.ProxyConfig.ProxyHTTPS = proxyHTTPS
	config.ProxyConfig.CertConfig.GenerateCert = true
	config.ProxyConfig.CertConfig.CertPath = "/tmp/p2pcert.pem"
	config.ProxyConfig.CertConfig.KeyPath = "/tmp/p2pcert.key"
	// p2p
	config.P2PConfig.RunMode = runMode
	config.P2PConfig.RootList = rootList
	config.P2PConfig.NodeIP = "127.0.0.1"
	config.P2PConfig.DetectAddr = ""
	config.P2PConfig.ServeBySSL = serveBySSL
	config.P2PConfig.Port = port
	config.P2PConfig.CacheConfig.MemCacheSize = 1 * 1024 * 1024 * 1024
	config.P2PConfig.CacheConfig.FileCacheSize = 1 * 1024 * 1024 * 1024
	config.P2PConfig.CacheConfig.FileCachePath = fmt.Sprintf("%s/%d", Media, idx)
	config.P2PConfig.PrefetchConfig.PrefetchEnable = true
	config.P2PConfig.PrefetchConfig.PrefetchThread = 64
	configure.CheckConfig(config)
	proxyServer := StartProxyServer(config, false)
	p2pServer := StartP2PServer(config, false)
	go func() {
		if err := proxyServer.ListenAndServe(); err != nil {
			log.Infof("Proxy server %s on port %d exit! %s", runMode, port+100, err)
		}
	}()
	go func() {
		if config.P2PConfig.ServeBySSL {
			certificate.GetRootCA(config.ProxyConfig.CertConfig.CertPath, config.ProxyConfig.CertConfig.KeyPath, config.ProxyConfig.CertConfig.GenerateCert)
			cert, key := certificate.GenerateCertificate(config.P2PConfig.NodeIP)
			if err := ioutil.WriteFile("/tmp/httpcert.pem", cert, 0644); err != nil {
				log.Fatalf("Write file httpcert.pem failed! %s", err)
			}
			if err := ioutil.WriteFile("/tmp/httpcert.key", key, 0644); err != nil {
				log.Fatalf("Write file httpcert.key failed! %s", err)
			}
			if err := p2pServer.ListenAndServeTLS("/tmp/httpcert.pem", "/tmp/httpcert.key"); err != nil {
				log.Infof("P2P server %s on port %d exit! %s", runMode, port, err)
			}
		} else {
			if err := p2pServer.ListenAndServe(); err != nil {
				log.Infof("P2P server %s on port %d exit! %s", runMode, port, err)
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		address := fmt.Sprintf("127.0.0.1:%d", port)
		for !util.CheckTCPConn(address) {
			log.Infof("Check address %s failed! retry...", address)
			time.Sleep(time.Second)
		}
		log.Infof("Start p2p server(%s) on port %d success!", runMode, port)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		address := fmt.Sprintf("127.0.0.1:%d", port+100)
		for !util.CheckTCPConn(address) {
			log.Infof("Check address %s failed! retry...", address)
			time.Sleep(time.Second)
		}
		log.Infof("Start proxy server on port %d success!", port+100)
	}()
	ConfigureCA()
	return p2pServer
}

func StartServers(root, agent int, serveBySSL, proxyHTTPS bool) []*http.Server {
	var res []*http.Server
	var rootList []string
	port := 19141 + rand.Intn(1000)
	for i := 0; i < root; i++ {
		res = append(res, StartServer(port, "root", nil, port, serveBySSL, proxyHTTPS))
		rootList = append(rootList, fmt.Sprintf("127.0.0.1:%d", port))
		port++
	}
	for i := 0; i < agent; i++ {
		tmp := make([]string, len(rootList))
		copy(tmp, rootList)
		res = append(res, StartServer(port, "agent", tmp, port, serveBySSL, proxyHTTPS))
		port++
	}
	// set level there!!!
	log.SetLevel(log.DebugLevel)
	return res
}

func GetData() string {
	const blockSize = 1024 * 1024
	length := rand.Intn(4) * blockSize
	length += rand.Intn(blockSize)
	return util.GetRandomString(length)
}

// content-length support range
func httpHandle1(w http.ResponseWriter, r *http.Request) {
	url := fmt.Sprintf("%s://%s%s", r.URL.Scheme, r.URL.Host, r.URL.Path)
	log.Infof("HTTP server accept request %s %s", r.Method, url)
	// TODO support server fail
	//if rand.Intn(10) == 0 {
	//	return
	//}
	rr, _ := requestMap.LoadOrStore(r.URL.Path, GetData())
	res := rr.(string)
	w.Header().Set("Accept-Ranges", "bytes")
	rangeStr := r.Header.Get("Range")
	if rangeStr != "" {
		rr := strings.Split(rangeStr, "=")
		rrr := strings.Split(rr[1], "-")
		left, _ := strconv.Atoi(rrr[0])
		right, _ := strconv.Atoi(rrr[1])
		w.Header().Set("Content-Length", fmt.Sprintf("%d", right-left+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", left, right, len(res)))
		w.WriteHeader(206)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(res[left : right+1]))
		}
	} else {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(res)))
		w.WriteHeader(200)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(res))
		}
	}
}

func startHTTPServer(port int, handle func(http.ResponseWriter, *http.Request)) *http.Server {
	addr := fmt.Sprintf(":%d", port)
	httpServer := &http.Server{Addr: addr, Handler: http.HandlerFunc(handle)}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil {
			log.Infof("HTTP Server exit! %s", err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		address := fmt.Sprintf("127.0.0.1:%d", port)
		for !util.CheckTCPConn(address) {
			log.Infof("Check address %s failed! retry...", address)
			time.Sleep(time.Second)
		}
		log.Info("Start HTTP Server success!")
	}()
	return httpServer
}

func startHTTPSServer(port int, handle func(http.ResponseWriter, *http.Request)) *http.Server {
	certificate.GetRootCA("/tmp/p2pcert.pem", "/tmp/p2pcert.key", true)
	cert, key := certificate.GenerateCertificate("127.0.0.1")
	if err := ioutil.WriteFile("/tmp/httpcert.pem", cert, 0644); err != nil {
		log.Fatalf("Write file httpcert.pem failed! %s", err)
	}
	if err := ioutil.WriteFile("/tmp/httpcert.key", key, 0644); err != nil {
		log.Fatalf("Write file httpcert.key failed! %s", err)
	}
	addr := fmt.Sprintf(":%d", port)
	httpServer := &http.Server{Addr: addr, Handler: http.HandlerFunc(handle)}
	go func() {
		if err := httpServer.ListenAndServeTLS("/tmp/httpcert.pem", "/tmp/httpcert.key"); err != nil {
			log.Infof("HTTPS Server exit! %s", err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		address := fmt.Sprintf("127.0.0.1:%d", port)
		for !util.CheckTCPConn(address) {
			log.Infof("Check address %s failed! retry...", address)
			time.Sleep(time.Second)
		}
		log.Info("Start HTTPS Server success!")
	}()
	return httpServer
}

func StartHTTPServers() []*http.Server {
	var res []*http.Server
	res = append(res, startHTTPServer(19131, httpHandle1))
	return res
}

func StartHTTPSServers() []*http.Server {
	var res []*http.Server
	res = append(res, startHTTPSServer(19132, httpHandle1))
	return res
}

func ExecuteCmd(stdout bool, name string, cmd ...string) (string, int) {
	cl := exec.Command(name, cmd...)
	if stdout {
		cl.Stdout = os.Stdout
		cl.Stderr = os.Stderr
		_ = cl.Run()
		code := cl.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
		return "", code
	}
	r, _ := cl.Output()
	code := cl.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
	return string(r), code
}

func ConfigureCA() {
	res, _ := ExecuteCmd(false, "cat", "/etc/issue")
	if strings.Contains(res, "Ubuntu") {
		// ubuntu
		ExecuteCmd(true, "cp", "/tmp/p2pcert.pem", "/usr/local/share/ca-certificates/p2pcert.crt")
		ExecuteCmd(true, "update-ca-certificates")
	} else if strings.Contains(res, "Kernel") {
		// Centos
		ExecuteCmd(true, "cp", "/tmp/p2pcert.pem", "/etc/pki/ca-trust/source/anchors/p2pcert.crt")
		ExecuteCmd(true, "update-ca-trust")
	} else {
		log.Fatalf("Unknown system!")
	}
}

func ConfigureDocker(proxyAddress string) {
	// proxy
	proxyAddress = fmt.Sprintf("http://127.0.0.1%s", proxyAddress)
	s := fmt.Sprintf("[Service]\nEnvironment=\"HTTP_PROXY=%s\"\nEnvironment=\"HTTPS_PROXY=%s\"", proxyAddress, proxyAddress)
	dir := "/etc/systemd/system/docker.service.d"
	fn := "/etc/systemd/system/docker.service.d/proxy.conf"
	if err := os.MkdirAll(dir, 0644); err != nil {
		log.Fatalf("Mkdir %s fail! %s", dir, err)
	}
	file, err := os.OpenFile(fn, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		log.Fatalf("Open file %s fail! %s", fn, err)
	}
	if _, err := file.WriteString(s); err != nil {
		log.Fatalf("Write file %s fail! %s", fn, err)
	}
	if err := file.Close(); err != nil {
		log.Fatalf("Close file %s fail! %s", fn, err)
	}
	// restart
	ExecuteCmd(true, "systemctl", "daemon-reload")
	ExecuteCmd(true, "systemctl", "restart", "docker")
}
