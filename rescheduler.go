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

package main

import (
	goflag "flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/pusher/spot-rescheduler/drain"
	"github.com/pusher/spot-rescheduler/metrics"
	"github.com/pusher/spot-rescheduler/nodes"
	simulator "k8s.io/autoscaler/cluster-autoscaler/simulator"
	autoscaler_drain "k8s.io/autoscaler/cluster-autoscaler/utils/drain"
	kube_utils "k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	clientv1 "k8s.io/client-go/pkg/api/v1"
	kube_restclient "k8s.io/client-go/rest"
	kube_record "k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/api"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	policyv1 "k8s.io/kubernetes/pkg/apis/policy/v1beta1"
	kube_client "k8s.io/kubernetes/pkg/client/clientset_generated/clientset"
	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/plugin/pkg/scheduler/schedulercache"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	flag "github.com/spf13/pflag"
)

const (
	// TaintsAnnotationKey represents the key of taints data (json serialized)
	// in the Annotations of a Node.
	TaintsAnnotationKey string = "scheduler.alpha.kubernetes.io/taints"
)

var (
	flags = flag.NewFlagSet(
		`rescheduler: rescheduler --running-in-cluster=true`,
		flag.ExitOnError)

	inCluster = flags.Bool("running-in-cluster", true,
		`Optional, if this controller is running in a kubernetes cluster, use the
		 pod secrets for creating a Kubernetes client.`)

	contentType = flags.String("kube-api-content-type", "application/vnd.kubernetes.protobuf",
		`Content type of requests sent to apiserver.`)

	housekeepingInterval = flags.Duration("housekeeping-interval", 10*time.Second,
		`How often rescheduler takes actions.`)

	nodeDrainDelay = flags.Duration("node-drain-delay", 10*time.Minute,
		`How long the scheduler should wait between draining nodes.`)

	podScheduledTimeout = flags.Duration("pod-scheduled-timeout", 10*time.Minute,
		`How long should rescheduler wait for critical pod to be scheduled
		 after evicting pods to make a spot for it.`)

	listenAddress = flags.String("listen-address", "localhost:9235",
		`Address to listen on for serving prometheus metrics`)
)

func main() {
	flags.AddGoFlagSet(goflag.CommandLine)

	// Log to stderr by default and fix usage message accordingly
	logToStdErr := flags.Lookup("logtostderr")
	logToStdErr.DefValue = "true"
	flags.Set("logtostderr", "true")

	// Add nodes labels as flags
	flags.StringVar(&nodes.OnDemandNodeLabel,
		"on-demand-node-label",
		"node-role.kubernetes.io/worker",
		`Name of label on nodes to be considered for draining.`)
	flags.StringVar(&nodes.SpotNodeLabel,
		"spot-node-label",
		"node-role.kubernetes.io/spot-worker",
		`Name of label on nodes to be considered as targets for pods.`)

	flags.Parse(os.Args)

	glog.Infof("Running Rescheduler")

	// Register metrics from metrics.go
	go func() {
		http.Handle("/metrics", prometheus.Handler())
		err := http.ListenAndServe(*listenAddress, nil)
		glog.Fatalf("Failed to start metrics: %v", err)
	}()

	lastDrainTime := time.Now().Add(-*nodeDrainDelay)

	kubeClient, err := createKubeClient(flags, *inCluster)
	if err != nil {
		glog.Fatalf("Failed to create kube client: %v", err)
	}

	recorder := createEventRecorder(kubeClient)

	stopChannel := make(chan struct{})

	// Predicate checker from K8s scheduler works out if a Pod could schedule onto a node
	predicateChecker, err := simulator.NewPredicateChecker(kubeClient, stopChannel)
	if err != nil {
		glog.Fatalf("Failed to create predicate checker: %v", err)
	}

	nodeLister := kube_utils.NewReadyNodeLister(kubeClient, stopChannel)
	podDisruptionBudgetLister := kube_utils.NewPodDisruptionBudgetLister(kubeClient, stopChannel)
	unschedulablePodLister := kube_utils.NewUnschedulablePodLister(kubeClient, stopChannel)

	// TODO(piosz): consider reseting this set once every few hours.
	//podsBeingProcessed := NewPodSet()

	for {
		select {
		// Run forever, every housekeepingInterval seconds
		case <-time.After(*housekeepingInterval):
			{
				if time.Since(lastDrainTime) < *nodeDrainDelay {
					glog.Infof("Waiting %s for drain delay timer.", *nodeDrainDelay-time.Since(lastDrainTime))
					continue
				}

				// Don't run if pods are unschedulable
				unschedulablePods, err := unschedulablePodLister.List()
				if err != nil {
					glog.Errorf("Failed to get unschedulable pods: %v", err)
				}
				if len(unschedulablePods) > 0 {
					glog.Info("Waiting for unschedulable pods to be scheduled.")
					continue
				}

				glog.Info("Starting node processing.")

				// All nodes in the cluster
				allNodes, err := nodeLister.List()
				if err != nil {
					glog.Errorf("Failed to list nodes: %v", err)
					continue
				}

				// Build a map of nodeInfo structs
				nodeMap, err := nodes.NewNodeMap(kubeClient, allNodes)
				if err != nil {
					glog.Errorf("Failed to build node map; %v", err)
					continue
				}

				metrics.UpdateNodesMap(nodeMap)

				// Get PodDisruptionBudgets
				// All nodes in the cluster
				allPDBs, err := podDisruptionBudgetLister.List()
				if err != nil {
					glog.Errorf("Failed to list PDBs: %v", err)
					continue
				}

				// Get onDemand and spot nodeInfos
				onDemandNodeInfos := nodeMap[nodes.OnDemand]
				spotNodeInfos := nodeMap[nodes.Spot]

				// Update spot node metrics
				updateSpotNodeMetrics(spotNodeInfos, allPDBs)

				if len(onDemandNodeInfos) < 1 {
					glog.Info("No nodes to process.")
				}

				// Go through each onDemand node in turn
				// Check each pod to see if it can be moved
				// In the case that all can be moved, drain the node
				for _, nodeInfo := range onDemandNodeInfos {

					// Create a copy of the spotNodeInfos so that we can modify the list
					// of pods within this node's iteration only
					nodePlan, err := spotNodeInfos.CopyNodeInfos(kubeClient)
					if err != nil {
						glog.Errorf("Failed to build plan; %v", err)
						continue
					}

					// Get a list of pods that we would need to move onto other nodes
					podsForDeletion, err := autoscaler_drain.GetPodsForDeletionOnNodeDrain(nodeInfo.Pods, allPDBs, false, false, false, false, nil, 0, time.Now())
					if err != nil {
						glog.Errorf("Failed to get pods for consideration: %v", err)
						continue
					}

					metrics.UpdateNodePodsCount(nodes.OnDemandNodeLabel, nodeInfo.Node.Name, len(podsForDeletion))
					if len(podsForDeletion) < 1 {
						// Nothing to do here
						glog.Infof("No pods on %s, skipping.", nodeInfo.Node.Name)
						continue
					}

					// Variable to guard against draining node not fit for draining
					var unmoveablePods bool = false

					glog.Infof("Considering %s for removal", nodeInfo.Node.Name)
					// Consider each pod in turn
					for _, pod := range podsForDeletion {

						// Works out if a spot node is available for rescheduling
						spotNodeInfo := findSpotNodeForPod(kubeClient, predicateChecker, nodePlan, pod)
						if spotNodeInfo == nil {
							glog.Infof("Pod %s can't be rescheduled on any existing spot node. This node cannot be drained.", podId(pod))
							unmoveablePods = true
							break
						} else {
							glog.Infof("Pod %s can be rescheduled on %v, adding to plan.", podId(pod), spotNodeInfo.Node.ObjectMeta.Name)
							spotNodeInfo.AddPod(kubeClient, pod)
						}
					}

					// If no unmoveable pods were found, drain node
					if !unmoveablePods {
						glog.Infof("All pods on %v can be moved. Will drain node.", nodeInfo.Node.Name)
						// Drain the node - places eviction on each pod moving them in turn.
						err := drain.DrainNode(nodeInfo.Node, podsForDeletion, kubeClient, recorder, 60, drain.MaxPodEvictionTime, drain.EvictionRetryTime)
						if err != nil {
							glog.Errorf("Failed to drain node: %v", err)
							metrics.UpdateNodeDrainCount("Failure", nodeInfo.Node.Name)
						} else {
							metrics.UpdateNodeDrainCount("Success", nodeInfo.Node.Name)
						}
						lastDrainTime = time.Now()
						break
					}
				}

				glog.Info("Finished processing nodes.")
			}
		}
	}
}

// Configure the kube client used to access the api, either from kubeconfig or
//from pod environment if running in the cluster
func createKubeClient(flags *flag.FlagSet, inCluster bool) (kube_client.Interface, error) {
	var config *kube_restclient.Config
	var err error
	if inCluster {
		config, err = kube_restclient.InClusterConfig()
	} else {
		clientConfig := kubectl_util.DefaultClientConfig(flags)
		config, err = clientConfig.ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("error connecting to the client: %v", err)
	}
	config.ContentType = *contentType
	return kube_client.NewForConfigOrDie(config), nil
}

// Create an event broadcaster so that we can call events when we modify the system
func createEventRecorder(client kube_client.Interface) kube_record.EventRecorder {
	eventBroadcaster := kube_record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(client.CoreV1().RESTClient()).Events("")})
	return eventBroadcaster.NewRecorder(api.Scheme, clientv1.EventSource{Component: "rescheduler"})
}

// Determines if any of the nodes meet the predicates that allow the Pod to be
// scheduled on the node, and returns the node if it finds a suitable one.
// Currently sorts nodes by most requested CPU in an attempt to fill fuller
// nodes first (Attempting to bin pack)
func findSpotNodeForPod(client kube_client.Interface, predicateChecker *simulator.PredicateChecker, nodeInfos []*nodes.NodeInfo, pod *apiv1.Pod) *nodes.NodeInfo {
	for _, nodeInfo := range nodeInfos {
		kubeNodeInfo := schedulercache.NewNodeInfo(nodeInfo.Pods...)
		kubeNodeInfo.SetNode(nodeInfo.Node)

		// Pretend pod isn't scheduled
		pod.Spec.NodeName = ""

		// Check with the schedulers predicates to find a node to schedule on
		if err := predicateChecker.CheckPredicates(pod, kubeNodeInfo); err == nil {
			return nodeInfo
		}
	}
	return nil
}

// Goes through a list of NodeInfos and updates the metrics system with the
// number of pods that the rescheduler understands (So not daemonsets for
// instance) that are on each of the nodes, labelling them as spot nodes.
func updateSpotNodeMetrics(spotNodeInfos nodes.NodeInfoArray, pdbs []*policyv1.PodDisruptionBudget) {
	for _, nodeInfo := range spotNodeInfos {
		// Get a list of pods that are on the node (Only the types considered by the rescheduler)
		podsOnNode, err := autoscaler_drain.GetPodsForDeletionOnNodeDrain(nodeInfo.Pods, pdbs, false, false, false, false, nil, 0, time.Now())
		if err != nil {
			glog.Errorf("Failed to get pods on spot node: %v", err)
			continue
		}
		metrics.UpdateNodePodsCount(nodes.SpotNodeLabel, nodeInfo.Node.Name, len(podsOnNode))

	}
}
