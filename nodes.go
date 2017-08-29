package main

import (
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	kube_client "k8s.io/kubernetes/pkg/client/clientset_generated/clientset"
)

const (
	onDemand NodeType = 0
	spot     NodeType = 1
)

type NodeInfo struct {
	node         *apiv1.Node
	pods         []*apiv1.Pod
	requestedCPU int64
	freeCPU      int64
}

type NodeType int

type NodeInfoArray []*NodeInfo

type NodesMap map[NodeType]NodeInfoArray

func newNodeMap(client kube_client.Interface, nodes []*apiv1.Node) (NodesMap, error) {
	nodeMap := NodesMap{
		onDemand: make([]*NodeInfo, 0),
		spot:     make([]*NodeInfo, 0),
	}

	for _, node := range nodes {
		nodeInfo, err := newNodeInfo(client, node)
		if err != nil {
			return nil, err
		}
		switch true {
		case isSpotNode(node):
			nodeMap[spot] = append(nodeMap[spot], nodeInfo)
			continue
		case isWorkerNode(node):
			nodeMap[onDemand] = append(nodeMap[onDemand], nodeInfo)
			continue
		default:
			continue
		}
	}

	sort.Slice(nodeMap[spot], func(i, j int) bool {
		return nodeMap[spot][i].requestedCPU > nodeMap[spot][j].requestedCPU
	})
	sort.Slice(nodeMap[onDemand], func(i, j int) bool {
		return nodeMap[onDemand][i].requestedCPU < nodeMap[onDemand][j].requestedCPU
	})

	return nodeMap, nil
}

func newNodeInfo(client kube_client.Interface, node *apiv1.Node) (*NodeInfo, error) {
	pods, err := getPodsOnNode(client, node)
	if err != nil {
		return nil, err
	}
	requestedCPU := calculateRequestedCPU(client, pods)

	return &NodeInfo{
		node:         node,
		pods:         pods,
		requestedCPU: requestedCPU,
		freeCPU:      node.Status.Capacity.Cpu().MilliValue() - requestedCPU,
	}, nil
}

func (n *NodeInfo) addPod(client kube_client.Interface, pod *apiv1.Pod) {
	n.pods = append(n.pods, pod)
	n.requestedCPU = calculateRequestedCPU(client, n.pods)
	n.freeCPU = n.node.Status.Capacity.Cpu().MilliValue() - n.requestedCPU
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

// Works out requested CPU for a collection of pods and returns it in MilliValue
// (Pod requests are stored as MilliValues hence the return type here)
func calculateRequestedCPU(client kube_client.Interface, pods []*apiv1.Pod) int64 {
	var CPURequests int64 = 0
	for _, pod := range pods {
		CPURequests += getPodCPURequests(pod)
	}
	return CPURequests
}

// Returns the total requested CPU  for all of the containers in a given Pod.
// (Returned as MilliValues)
func getPodCPURequests(pod *apiv1.Pod) int64 {
	var CPUTotal int64 = 0
	if len(pod.Spec.Containers) > 0 {
		for _, container := range pod.Spec.Containers {
			CPUTotal += container.Resources.Requests.Cpu().MilliValue()
		}
	}
	return CPUTotal
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

func (n NodeInfoArray) deepCopy(client kube_client.Interface) (NodeInfoArray, error) {
	var arr NodeInfoArray
	for _, node := range n {
		nodeInfo, err := newNodeInfo(client, node.node)
		if err != nil {
			return nil, err
		}
		arr = append(arr, nodeInfo)
	}
	return arr, nil
}
