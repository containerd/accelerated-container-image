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

package fs

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/hostselector"

	log "github.com/sirupsen/logrus"
)

// FetchFailure error type for remote access failure, stores http response
type FetchFailure struct {
	Resp *http.Response
	Err  error
}

func (f FetchFailure) Error() string {
	return f.Err.Error()
}

// RemoteFetcher is interface for fetching source data via remote access
type RemoteFetcher interface {
	// PreadRemote PRead like method to fetch file data starts from offset, length as `len(buf)`
	PreadRemote(buf []byte, offset int64) (int, error)
	// FstatRemote get file length by remote
	FstatRemote() (int64, error)
}

// remoteSource is a RemoteFetcher implementation
type remoteSource struct {
	req       *http.Request
	hp        hostselector.HostPicker
	apikey    string
	transport *http.Transport
}

// newRemoteSource will new a remoteSource
func newRemoteSource(req *http.Request, hp hostselector.HostPicker, APIKey string) RemoteFetcher {
	return &remoteSource{
		req:    req,
		hp:     hp,
		apikey: APIKey,
		transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			MaxConnsPerHost: 100,
		},
	}
}

func (f *remoteSource) PreadRemote(buff []byte, offset int64) (int, error) {
	fn := f.req.URL.String()
	upperHost := f.hp.GetHost(fn)
	url := fn
	if upperHost != "" {
		url = fmt.Sprintf("%s/%s/%s", upperHost, f.apikey, fn)
	}
	newReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return -1, err
	}
	for k, vv := range f.req.Header {
		vv2 := make([]string, len(vv))
		copy(vv2, vv)
		newReq.Header[k] = vv2
	}
	newReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, int64(len(buff))+offset-1))
	log.Infof("Fetching remote %s %s", fn, newReq.Header.Get("Range"))
	client := http.Client{
		Transport: f.transport,
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(newReq)
	if err != nil || (resp.StatusCode != 200 && resp.StatusCode != 206) {
		log.Error(resp, err)
		return 0, FetchFailure{resp, err}
	}
	source := resp.Header.Get("X-P2P-Source")
	if source != "" {
		f.hp.PutHost(fn, source)
	} else {
		source = "origin"
	}
	log.Infof("Got remote %s %s from %s ", fn, newReq.Header.Get("Range"), source)
	return io.ReadFull(resp.Body, buff)
}

func (f *remoteSource) FstatRemote() (int64, error) {
	url := f.req.URL.String()
	newReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, FetchFailure{nil, err}
	}
	for k, vs := range f.req.Header {
		for _, v := range vs {
			newReq.Header.Add(k, v)
		}
	}
	newReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 0, 0))
	newReq.Header.Del("X-P2P-Agent")
	client := http.Client{
		Transport: f.transport,
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(newReq)
	if err != nil || (resp.StatusCode != 200 && resp.StatusCode != 206) {
		return 0, FetchFailure{resp, err}
	}
	if resp.StatusCode == 200 {
		return resp.ContentLength, err
	}
	l := resp.ContentLength
	rs := resp.Header.Get("Content-Range")
	if rs == "" {
		return l, err
	}
	pos := strings.LastIndexByte(rs, '/')
	if pos < 0 {
		return l, err
	}
	l, _ = strconv.ParseInt(rs[pos+1:], 10, 64)
	return l, err
}
