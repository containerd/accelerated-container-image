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

package metrics

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ExporterConfig struct {
	Enable    bool   `json:"enable"`
	UriPrefix string `json:"uriPrefix"`
	Port      int    `json:"port"`
}

var (
	Config ExporterConfig

	IsAlive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "is_alive",
		Help: "Indicates whether overlaybd-snapshotter is running or not.",
	})
	GRPCErrCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_error_count",
		Help: "Error count of GRPC APIs.",
	}, []string{"function"})
	GRPCLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_latency_seconds",
		Help:    "Latency of GRPC APIs.",
		Buckets: prometheus.ExponentialBuckets(0.0001, 2, 20),
	}, []string{"function"})
)

func Init() {
	http.Handle(Config.UriPrefix, promhttp.Handler())
	http.ListenAndServe(":"+fmt.Sprintf("%d", Config.Port), nil)
}
