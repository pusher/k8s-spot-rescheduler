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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	kube_leaderelection "k8s.io/kubernetes/pkg/client/leaderelection"
	"k8s.io/kubernetes/pkg/client/leaderelection/resourcelock"
	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/plugin/pkg/scheduler/schedulercache"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	flag "github.com/spf13/pflag"
)

var (
	flags = flag.NewFlagSet(
		`rescheduler: rescheduler --running-in-cluster=true`,
		flag.ExitOnError)

	inCluster = flags.Bool("running-in-cluster", true,
		`Optional, if this controller is running in a kubernetes cluster, use the
		 pod secrets for creating a Kubernetes client.`)

	namespace = flag.String("namespace", "kube-system",
		`Namespace in which spot-rescheduler is run`)

	contentType = flags.String("kube-api-content-type", "application/vnd.kubernetes.protobuf",
		`Content type of requests sent to apiserver.`)

	housekeepingInterval = flags.Duration("housekeeping-interval", 10*time.Second,
		`How often rescheduler takes actions.`)

	nodeDrainDelay = flags.Duration("node-drain-delay", 10*time.Minute,
		`How long the scheduler should wait between draining nodes.`)

	podEvictionTimeout = flags.Duration("pod-eviction-timeout", 2*time.Minute,
		`How long should the rescheduler attempt to retrieve successful pod
		 evictions for.`)

	maxGracefulTermination = flags.Duration("max-graceful-termination", 2*time.Minute,
		`How long should the rescheduler wait for pods to shutdown gracefully before
		 failing the node drain attempt.`)

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

	kubeClient, err := createKubeClient(flags, *inCluster)
	if err != nil {
		glog.Fatalf("Failed to create kube client: %v", err)
	}

	recorder := createEventRecorder(kubeClient)

	leaderElection := kube_leaderelection.DefaultLeaderElectionConfiguration()
	if *inCluster {
		leaderElection.LeaderElect = true
	}

	if !leaderElection.LeaderElect {
		run(kubeClient, recorder)
	} else {
		id, err := os.Hostname()
		if err != nil {
			glog.Fatalf("Unable to get hostname: %v", err)
		}
		kube_leaderelection.RunOrDie(kube_leaderelection.LeaderElectionConfig{
			Lock: &resourcelock.EndpointsLock{
				EndpointsMeta: metav1.ObjectMeta{
					Namespace: *namespace,
					Name:      "spot-rescheduler",
				},
				Client: kubeClient.CoreV1(),
				LockConfig: resourcelock.ResourceLockConfig{
					Identity:      id,
					EventRecorder: recorder,
				},
			},
			LeaseDuration: leaderElection.LeaseDuration.Duration,
			RenewDeadline: leaderElection.RenewDeadline.Duration,
			RetryPeriod:   leaderElection.RetryPeriod.Duration,
			Callbacks: kube_leaderelection.LeaderCallbacks{
				OnStartedLeading: func(_ <-chan struct{}) {
					// Since we are committing a suicide after losing
					// mastership, we can safely ignore the argument.
					run(kubeClient, recorder)
				},
				OnStoppedLeading: func() {
					glog.Fatalf("Lost leader status, terminating.")
				},
			},
		})

	}

}

func run(kubeClient kube_client.Interface, recorder kube_record.EventRecorder) {

	stopChannel := make(chan struct{})

	// Predicate checker from K8s scheduler works out if a Pod could schedule onto a node
	predicateChecker, err := simulator.NewPredicateChecker(kubeClient, stopChannel)
	if err != nil {
		glog.Fatalf("Failed to create predicate checker: %v", err)
	}

	nodeLister := kube_utils.NewReadyNodeLister(kubeClient, stopChannel)
	podDisruptionBudgetLister := kube_utils.NewPodDisruptionBudgetLister(kubeClient, stopChannel)
	unschedulablePodLister := kube_utils.NewUnschedulablePodLister(kubeClient, stopChannel)

	nextDrainTime := time.Now()

	for {
		select {
		// Run forever, every housekeepingInterval seconds
		case <-time.After(*housekeepingInterval):
			{
				// Don't do anything if we are waiting for the drain delay timer
				if time.Until(nextDrainTime) > 0 {
					glog.V(2).Infof("Waiting %s for drain delay timer.", time.Until(nextDrainTime))
					continue
				}

				// Don't run if pods are unschedulable
				unschedulablePods, err := unschedulablePodLister.List()
				if err != nil {
					glog.Errorf("Failed to get unschedulable pods: %v", err)
				}
				if len(unschedulablePods) > 0 {
					glog.V(2).Info("Waiting for unschedulable pods to be scheduled.")
					continue
				}

				glog.V(3).Info("Starting node processing.")

				// Get all nodes in the cluster
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

				// Update metrics
				metrics.UpdateNodesMap(nodeMap)

				// Get PodDisruptionBudgets
				allPDBs, err := podDisruptionBudgetLister.List()
				if err != nil {
					glog.Errorf("Failed to list PDBs: %v", err)
					continue
				}

				// Get onDemand and spot nodeInfoArrays
				onDemandNodeInfos := nodeMap[nodes.OnDemand]
				spotNodeInfos := nodeMap[nodes.Spot]

				// Update spot node metrics
				updateSpotNodeMetrics(spotNodeInfos, allPDBs)

				if len(onDemandNodeInfos) < 1 {
					glog.V(2).Info("No nodes to process.")
				}

				// Go through each onDemand node in turn
				// Build a plan to move pods onto other nodes
				// In the case that all can be moved, drain the node
				for _, nodeInfo := range onDemandNodeInfos {

					// Get a list of pods that we would need to move onto other nodes
					podsForDeletion, err := autoscaler_drain.GetPodsForDeletionOnNodeDrain(nodeInfo.Pods, allPDBs, false, false, false, false, nil, 0, time.Now())
					if err != nil {
						glog.Errorf("Failed to get pods for consideration: %v", err)
						continue
					}

					// Update the number of pods on this node's metrics
					metrics.UpdateNodePodsCount(nodes.OnDemandNodeLabel, nodeInfo.Node.Name, len(podsForDeletion))
					if len(podsForDeletion) < 1 {
						// Nothing to do here
						glog.V(2).Infof("No pods on %s, skipping.", nodeInfo.Node.Name)
						continue
					}

					glog.V(2).Infof("Considering %s for removal", nodeInfo.Node.Name)

					// Checks whether or not a node can be drained
					err = canDrainNode(predicateChecker, spotNodeInfos, podsForDeletion)
					if err != nil {
						glog.V(2).Infof("Cannot drain node: %v", err)
						continue
					}

					// If building plan was successful, can drain node.
					glog.V(2).Infof("All pods on %v can be moved. Will drain node.", nodeInfo.Node.Name)
					// Drain the node - places eviction on each pod moving them in turn.
					err = drainNode(kubeClient, recorder, nodeInfo.Node, podsForDeletion, int(maxGracefulTermination.Seconds()), *podEvictionTimeout)
					if err != nil {
						glog.Errorf("Failed to drain node: %v", err)
					}
					nextDrainTime = time.Now().Add(*nodeDrainDelay)
					break
				}

				glog.V(3).Info("Finished processing nodes.")
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
func findSpotNodeForPod(predicateChecker *simulator.PredicateChecker, nodeInfos []*nodes.NodeInfo, pod *apiv1.Pod) *nodes.NodeInfo {
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

// Goes through a list of pods and works out new nodes to place them on.
// Returns an error if any of the pods won't fit onto existing spot nodes.
func canDrainNode(predicateChecker *simulator.PredicateChecker, nodeInfos nodes.NodeInfoArray, pods []*apiv1.Pod) error {
	// Create a copy of the nodeInfos so that we can modify the list within this
	// call
	nodePlan := nodeInfos.CopyNodeInfos()

	for _, pod := range pods {
		// Works out if a spot node is available for rescheduling
		spotNodeInfo := findSpotNodeForPod(predicateChecker, nodePlan, pod)
		if spotNodeInfo == nil {
			return fmt.Errorf("Pod %s can't be rescheduled on any existing spot node.", podId(pod))
		} else {
			glog.V(4).Infof("Pod %s can be rescheduled on %v, adding to plan.", podId(pod), spotNodeInfo.Node.ObjectMeta.Name)
			spotNodeInfo.AddPod(pod)
		}
	}

	return nil
}

// Performs a drain on given node and updates the nextDrainTime variable.
// Returns an error if the drain fails.
func drainNode(kubeClient kube_client.Interface, recorder kube_record.EventRecorder, node *apiv1.Node, pods []*apiv1.Pod, maxGracefulTermination int, podEvictionTimeout time.Duration) error {
	err := drain.DrainNode(node, pods, kubeClient, recorder, maxGracefulTermination, podEvictionTimeout, drain.EvictionRetryTime)
	if err != nil {
		metrics.UpdateNodeDrainCount("Failure", node.Name)
		return err
	}

	metrics.UpdateNodeDrainCount("Success", node.Name)
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
