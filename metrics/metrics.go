/*
Copyright 2017 The Kubernetes Authors.

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
	"github.com/prometheus/client_golang/prometheus"
)

const (
	reschedulerNamespace = "spot_rescheduler"
)

var (
	// onDemandPodsCount tracks how many pods are on on-demand nodes.
	onDemandPodsCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "rescheduler",
			Name:      "on_demand_pods_count",
			Help:      "Number of times a critical pod was unschedulable.",
		},
		[]string{"node"})

	// nodesCount tracks the number of nodes in the cluster.
	nodesCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: reschedulerNamespace,
			Name:      "nodes_count",
			Help:      "Number of nodes in cluster.",
		}, []string{"node_type"},
	)

	// nodeDrainCount counts the number of nodes drained by the rescheduler.
	nodeDrainCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: reschedulerNamespace,
			Name:      "node_drain_total",
			Help:      "Number of nodes drained by CA.",
		}, []string{"drain_state", "node"},
	)

	// evictionsCount counts the number of pods evicted by the rescheduler
	evictionsCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: reschedulerNamespace,
			Name:      "evicted_pods_total",
			Help:      "Number of pods evicted by the rescheduler.",
		},
	)
)

func init() {
	prometheus.MustRegister(onDemandPodsCount)
	prometheus.MustRegister(nodesCount)
	prometheus.MustRegister(nodeDrainCount)
	prometheus.MustRegister(evictionsCount)
}
