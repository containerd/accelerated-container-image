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

type DeployConfig struct {
	LogLevel       string
	RunMode        string
	RootList       []string
	NodeIP         string
	DetectAddr     string
	ServeBySSL     bool
	Port           int
	CertConfig     DeployCertConfig
	CacheConfig    DeployCacheConfig
	PrefetchConfig DeployPrefetchConfig
}

type DeployCertConfig struct {
	CertEnable   bool
	GenerateCert bool
	CertPath     string
	KeyPath      string
}

type DeployCacheConfig struct {
	FileCacheEnable bool
	FileCacheSize   int64
	FileCachePath   string
	MemCacheEnable  bool
	MemCacheSize    int64
}

type DeployPrefetchConfig struct {
	PrefetchEnable bool
	PrefetchThread int
}
