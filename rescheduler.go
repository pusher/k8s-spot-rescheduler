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
	"encoding/json"
	goflag "flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	simulator "k8s.io/autoscaler/cluster-autoscaler/simulator"
	kube_utils "k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	clientv1 "k8s.io/client-go/pkg/api/v1"
	kube_restclient "k8s.io/client-go/rest"
	kube_record "k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/api"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/apis/policy/v1beta1"
	kube_client "k8s.io/kubernetes/pkg/client/clientset_generated/clientset"
	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/plugin/pkg/scheduler/schedulercache"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	flag "github.com/spf13/pflag"
)

const (
	workerNodeLabel = "node-role.kubernetes.io/worker"
	spotNodeLabel   = "node-role.kubernetes.io/spot-worker"
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

	stopChannel := make(chan struct{})

	// Predicate checker from K8s scheduler works out if a Pod could schedule onto a node
	predicateChecker, err := simulator.NewPredicateChecker(kubeClient, stopChannel)
	if err != nil {
		glog.Fatalf("Failed to create predicate checker: %v", err)
	}

	nodeLister := kube_utils.NewReadyNodeLister(kubeClient, stopChannel)
	podDisruptionBudgetLister := kube_utils.NewPodDisruptionBudgetLister(kubeClient, stopChannel)

	// TODO(piosz): consider reseting this set once every few hours.
	podsBeingProcessed := NewPodSet()

	for {
		select {
		// Run forever, every housekeepingInterval seconds
		case <-time.After(*housekeepingInterval):
			{

				// All nodes in the cluster
				allNodes, err := nodeLister.List()
				if err != nil {
					glog.Errorf("Failed to list nodes: %v", err)
					continue
				}

				// Filter all nodes down to just spot instances
				spotNodes := []*apiv1.Node{}
				workerNodes := []*apiv1.Node{}
				for _, node := range allNodes {
					if isSpotNode(node) {
						spotNodes = append(spotNodes, node)
					} else if isWorkerNode(node) {
						workerNodes = append(workerNodes, node)
					}
				}

				// Get pods on worker nodes
				workerNodePods := getPodsOnNodes(kubeClient, workerNodes, podsBeingProcessed)

				if len(workerNodePods) > 0 {
					// Consider each pod in turn
					for _, pod := range workerNodePods {
						glog.Infof("Found %s on a worker node", pod.Name)

						// Works out if a spot node is available for rescheduling
						node := findNodeForPod(kubeClient, predicateChecker, spotNodes, pod)
						if node == nil {
							glog.Infof("Pod %s can't be rescheduled on any existing spot node.", podId(pod))
							continue
						} else {
							glog.Infof("Pod %s can be rescheduled, attempting to reschedule.", podId(pod))
						}

						// Wokr out if a PDB would be broken if we delted the pod
						allowance, err := havePodDisruptionAllowance(podDisruptionBudgetLister, pod)
						if err != nil {
							glog.Errorf("Error while looking for PodDisruptionBudgets, %v", err)
							continue
						}
						if !allowance {
							glog.Infof("Cannot reschedule %s, not enough pod disruption budget.", podId(pod))
							continue
						}

						// Delete the pod and wait for it to be rescheduled before continuing to the next pod
						err = deletePod(kubeClient, recorder, pod, node)
						if err != nil {
							glog.Infof("Failed to delete %s; %s", pod.Name, err)
						} else {
							podsBeingProcessed.Add(pod)
							go waitForScheduled(kubeClient, podsBeingProcessed, pod)
						}

					}
				} else {
					glog.Infof("No pods to be considered for rescheduling.")
				}
			}
		}
	}
}

// Should wait (up to the timeout) for a pod to be rescheduled
// Blocker to slow down processing and make sure we only disturb one pod at a time
func waitForScheduled(client kube_client.Interface, podsBeingProcessed *podSet, pod *apiv1.Pod) {
	glog.Infof("Waiting for pod %s to be scheduled", podId(pod))
	err := wait.Poll(time.Second, *podScheduledTimeout, func() (bool, error) {
		p, err := client.CoreV1().Pods(pod.Namespace).Get(pod.Name, metav1.GetOptions{})
		if err != nil {
			glog.Warningf("Error while getting pod %s: %v", podId(pod), err)
			return false, nil
		}
		return p.Spec.NodeName != "", nil
	})
	if err != nil {
		glog.Warningf("Timeout while waiting for pod %s to be scheduled after %v.", podId(pod), *podScheduledTimeout)
	} else {
		glog.Infof("Pod %v was successfully scheduled.", podId(pod))
	}
	podsBeingProcessed.Remove(pod)
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

// This is unused? I don't know what it is... what is it doing here :(
func getTaintsFromNodeAnnotations(annotations map[string]string) ([]apiv1.Taint, error) {
	var taints []apiv1.Taint
	if len(annotations) > 0 && annotations[TaintsAnnotationKey] != "" {
		err := json.Unmarshal([]byte(annotations[TaintsAnnotationKey]), &taints)
		if err != nil {
			return []apiv1.Taint{}, err
		}
	}
	return taints, nil
}

// Deletes a pod and broadcasts the event to the event recorder
func deletePod(client kube_client.Interface, recorder kube_record.EventRecorder, pod *apiv1.Pod, node *apiv1.Node) error {
	glog.Infof("Deleting pod %s", pod.Name)
	recorder.Eventf(pod, apiv1.EventTypeNormal, "DeletedByRescheduler",
		"Deleted by rescheduler to remove load from on-demand instance")
	err := client.CoreV1().Pods(pod.Namespace).Delete(pod.Name, metav1.NewDeleteOptions(10))
	if err != nil {
		return fmt.Errorf("Failed to delete pod %s: %v", podId(pod), err)
	}
	return nil
}

// Takes a copy of all of the details of the node
func copyNode(node *apiv1.Node) (*apiv1.Node, error) {
	objCopy, err := api.Scheme.DeepCopy(node)
	if err != nil {
		return nil, err
	}
	copied, ok := objCopy.(*apiv1.Node)
	if !ok {
		return nil, fmt.Errorf("expected Node, got %#v", objCopy)
	}
	return copied, nil
}

// Determines if any of the nodes meet the predicates that allow the Pod to be
// scheduled on the node, and returns the node if it finds a suitable one.
// Currently sorts nodes by most requested CPU in an attempt to fill fuller
// nodes first (Attempting to bin pack)
func findNodeForPod(client kube_client.Interface, predicateChecker *simulator.PredicateChecker, nodes []*apiv1.Node, pod *apiv1.Pod) *apiv1.Node {
	// Sort the nodes by CPU most requested first (Least spare CPU)
	sort.Slice(nodes, func(i int, j int) bool {
		iCPU, _, err := getNodeSpareCapacity(client, nodes[i])
		if err != nil {
			glog.Errorf("Failed to find node capacity %v", err)
		}
		jCPU, _, err := getNodeSpareCapacity(client, nodes[j])
		if err != nil {
			glog.Errorf("Failed to find node capacity %v", err)
		}
		return iCPU < jCPU
	})

	for _, originalNode := range nodes {
		// Operate on a copy of the node to ensure pods running on the node will pass CheckPredicates below.
		node, err := copyNode(originalNode)
		if err != nil {
			glog.Errorf("Error while copying node: %v", err)
			continue
		}

		podsOnNode, err := getPodsOnNode(client, node)
		if err != nil {
			glog.Warningf("Skipping node %v due to error: %v", node.Name, err)
			continue
		}

		nodeInfo := schedulercache.NewNodeInfo(podsOnNode...)
		nodeInfo.SetNode(node)

		// Pretend pod isn't scheduled
		pod.Spec.NodeName = ""

		// Check with the schedulers predicates to find a node to schedule on
		if err := predicateChecker.CheckPredicates(pod, nodeInfo); err == nil {
			return node
		}
	}
	return nil
}

// Genereates a list of Pods that are running on worker nodes
// List is sorted such that pods come from emptier nodes first when being
// considered for rescheduling
func getPodsOnNodes(client kube_client.Interface, nodes []*apiv1.Node, podsBeingProcessed *podSet) []*apiv1.Pod {

	// Sort nodes by least requested CPU first (Greatest spare CPU)
	sort.Slice(nodes, func(i int, j int) bool {
		iCPU, _, err := getNodeSpareCapacity(client, nodes[i])
		if err != nil {
			glog.Errorf("Failed to find node capacity %v", err)
		}
		jCPU, _, err := getNodeSpareCapacity(client, nodes[j])
		if err != nil {
			glog.Errorf("Failed to find node capacity %v", err)
		}
		return iCPU > jCPU
	})

	// Gets pods running on these nodes that are managed by a ReplicaSet
	workerNodePods := []*apiv1.Pod{}
	for _, node := range nodes {
		podsOnNode, err := getPodsOnNode(client, node)
		if err != nil {
			glog.Errorf("Failed to find pods on %v", node.Name)
		}
		for _, pod := range podsOnNode {
			if isReplicaSetPod(pod) && !podsBeingProcessed.Has(pod) {
				workerNodePods = append(workerNodePods, pod)
			}
		}
	}

	return workerNodePods
}

// Determines if a pod is managed by a ReplicaSet
func isReplicaSetPod(pod *apiv1.Pod) bool {
	return len(pod.ObjectMeta.OwnerReferences) > 0 && pod.ObjectMeta.OwnerReferences[0].Kind == "ReplicaSet"
}

// Determines if a node has the spotNodeLabel assigned
func isSpotNode(node *apiv1.Node) bool {
	_, found := node.ObjectMeta.Labels[spotNodeLabel]
	return found
}

// Determines if a node has the workerNodeLabel assigned
func isWorkerNode(node *apiv1.Node) bool {
	_, found := node.ObjectMeta.Labels[workerNodeLabel]
	return found
}

// Gets a list of pods that are running on the given node
func getPodsOnNode(client kube_client.Interface, node *apiv1.Node) ([]*apiv1.Pod, error) {
	podsOnNode, err := client.CoreV1().Pods(apiv1.NamespaceAll).List(
		metav1.ListOptions{FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}).String()})
	if err != nil {
		return []*apiv1.Pod{}, err
	}

	pods := make([]*apiv1.Pod, 0)
	for i := range podsOnNode.Items {
		pods = append(pods, &podsOnNode.Items[i])
	}
	return pods, nil
}

// Works out spare CPU and Memory for a node and returns in MilliValues
// (Pod requests are stored as MilliValues hence the return type here)
func getNodeSpareCapacity(client kube_client.Interface, node *apiv1.Node) (int64, int64, error) {
	nodeCPU := node.Status.Capacity.Cpu().MilliValue()
	nodeMemory := node.Status.Capacity.Memory().MilliValue()

	podsOnNode, err := getPodsOnNode(client, node)
	if err != nil {
		return 0, 0, err
	}

	var CPURequests, MemoryRequests int64 = 0, 0

	for _, pod := range podsOnNode {
		podCPURequest, podMemoryRequest := getPodRequests(pod)
		CPURequests += podCPURequest
		MemoryRequests += podMemoryRequest
	}

	return nodeCPU - CPURequests, nodeMemory - MemoryRequests, nil
}

// Returns the total requested CPU and Memory for all of the containers in a
// given Pod. (Returned as MilliValues)
func getPodRequests(pod *apiv1.Pod) (int64, int64) {
	var CPUTotal, MemoryTotal int64 = 0, 0
	if len(pod.Spec.Containers) > 0 {
		for _, container := range pod.Spec.Containers {
			CPURequest := container.Resources.Requests.Cpu().MilliValue()
			MemoryRequest := container.Resources.Requests.Memory().MilliValue()

			CPUTotal += CPURequest
			MemoryTotal += MemoryRequest
		}
	}
	return CPUTotal, MemoryTotal
}

// Works out if there is allowance in a pod's disruption budgets for it to be delted
func havePodDisruptionAllowance(lister *kube_utils.PodDisruptionBudgetLister, pod *apiv1.Pod) (bool, error) {
	pdbs, err := getPodPodDisruptionBudgets(lister, pod)
	for _, pdb := range pdbs {
		if pdb.Status.PodDisruptionsAllowed < 1 {
			return false, err
		}
	}
	return true, err
}

// gets PodDisruptionBudgets associated with the given pod
func getPodPodDisruptionBudgets(lister *kube_utils.PodDisruptionBudgetLister, pod *apiv1.Pod) ([]*v1beta1.PodDisruptionBudget, error) {
	if len(pod.Labels) == 0 {
		return nil, fmt.Errorf("No PodDisruptionBudgets found for pod %v because it has no labels", pod.Name)
	}

	allPDBs, err := lister.List()
	if err != nil {
		return make([]*v1beta1.PodDisruptionBudget, 0), err
	}

	var selector labels.Selector

	pdbs := make([]*v1beta1.PodDisruptionBudget, 0)
	for _, pdb := range allPDBs {
		selector, err = metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			glog.Warningf("invalid selector: %v", err)
			continue
		}
		// If a PDB with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		pdbs = append(pdbs, pdb)
	}
	return pdbs, nil
}
