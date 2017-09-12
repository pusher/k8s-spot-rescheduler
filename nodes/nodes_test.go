package nodes

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	core "k8s.io/client-go/testing"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/client/clientset_generated/clientset/fake"
)

func TestNewNodeMap(t *testing.T) {
	spotLabels := map[string]string{
		SpotNodeLabel: "true",
	}
	onDemandLabels := map[string]string{
		OnDemandNodeLabel: "true",
	}

	nodes := []*apiv1.Node{
		createTestNodeWithLabel("node1", 2000, onDemandLabels),
		createTestNodeWithLabel("node2", 2000, onDemandLabels),
		createTestNodeWithLabel("node3", 2000, spotLabels),
		createTestNodeWithLabel("node4", 2000, spotLabels),
	}

	fakeClient := createFakeClient(t)

	nodeMap, err := NewNodeMap(fakeClient, nodes)
	if err != nil {
		assert.Error(t, err, "Failed to build nodeMap")
	}
	onDemandNodeInfos := nodeMap[OnDemand]
	spotNodeInfos := nodeMap[Spot]

	assert.Equal(t, 2, len(onDemandNodeInfos))
	assert.Equal(t, 2, len(spotNodeInfos))

	// The first spot node should be the one with least requestedCPU
	nodeInfo1 := onDemandNodeInfos[0]
	nodeInfo2 := onDemandNodeInfos[1]
	if nodeInfo1.RequestedCPU > nodeInfo2.RequestedCPU {
		assert.Fail(t, "Spot nodes not sorted by Free CPU")
	}

	assert.Equal(t, "node1", nodeInfo1.Node.Name)
	assert.Equal(t, 2, len(nodeInfo1.Pods))
	assert.Equal(t, "node2", nodeInfo2.Node.Name)
	assert.Equal(t, 3, len(nodeInfo2.Pods))

	// The first spot node should be the one with least freeCPU
	nodeInfo3 := spotNodeInfos[0]
	nodeInfo4 := spotNodeInfos[1]
	if nodeInfo3.FreeCPU > nodeInfo4.FreeCPU {
		assert.Fail(t, "Spot nodes not sorted by Free CPU")
	}

	// This means we should get node3 and node2 in this order
	assert.Equal(t, "node4", nodeInfo3.Node.Name)
	assert.Equal(t, 5, len(nodeInfo3.Pods))
	assert.Equal(t, "node3", nodeInfo4.Node.Name)
	assert.Equal(t, 2, len(nodeInfo4.Pods))

	// Check pods are sorted by Most RequestedCPU
	for _, nodeInfo := range append(onDemandNodeInfos, spotNodeInfos...) {
		for i := 1; i < len(nodeInfo.Pods); i++ {
			firstPodRequest := getPodCPURequests(nodeInfo.Pods[i-1])
			secondPodRequest := getPodCPURequests(nodeInfo.Pods[i])
			if firstPodRequest < secondPodRequest {
				assert.Fail(t, "Pods not sorted by most requested CPU on node %s", nodeInfo.Node.Name)
			}
		}
	}

}

func TestAddPod(t *testing.T) {

	nodeInfo1 := createTestNodeInfo(createTestNode("node1", 2000), []*apiv1.Pod{}, 0)
	pod1 := createTestPod("pod1", 300)
	nodeInfo1.AddPod(pod1)

	assert.Equal(t, 1, len(nodeInfo1.Pods))
	assert.Equal(t, int64(300), nodeInfo1.RequestedCPU)
	assert.Equal(t, int64(1700), nodeInfo1.FreeCPU)

	pod2 := createTestPod("pod2", 721)
	nodeInfo1.AddPod(pod2)

	assert.Equal(t, 2, len(nodeInfo1.Pods))
	assert.Equal(t, int64(1021), nodeInfo1.RequestedCPU)
	assert.Equal(t, int64(979), nodeInfo1.FreeCPU)
}

func TestGetPodsOnNode(t *testing.T) {
	node1 := createTestNode("node1", 2000)
	node2 := createTestNode("node2", 2000)
	node3 := createTestNode("node3", 2000)
	node4 := createTestNode("node4", 2000)

	fakeClient := createFakeClient(t)

	podsOnNode1, err := getPodsOnNode(fakeClient, node1)
	if err != nil {
		assert.Error(t, err, "Found error in getting pods on node")
	}
	assert.Equal(t, 2, len(podsOnNode1))
	assert.Equal(t, "p1n1", podsOnNode1[0].Name)
	assert.Equal(t, "p2n1", podsOnNode1[1].Name)

	podsOnNode2, err := getPodsOnNode(fakeClient, node2)
	if err != nil {
		assert.Error(t, err, "Found error in getting pods on node")
	}
	assert.Equal(t, 3, len(podsOnNode2))
	assert.Equal(t, "p1n2", podsOnNode2[0].Name)
	assert.Equal(t, "p2n2", podsOnNode2[1].Name)
	assert.Equal(t, "p3n2", podsOnNode2[2].Name)

	podsOnNode3, err := getPodsOnNode(fakeClient, node3)
	if err != nil {
		assert.Error(t, err, "Found error in getting pods on node")
	}
	assert.Equal(t, 2, len(podsOnNode3))
	assert.Equal(t, "p1n3", podsOnNode3[0].Name)
	assert.Equal(t, "p2n3", podsOnNode3[1].Name)

	podsOnNode4, err := getPodsOnNode(fakeClient, node4)
	if err != nil {
		assert.Error(t, err, "Found error in getting pods on node")
	}
	assert.Equal(t, 5, len(podsOnNode4))
	assert.Equal(t, "p1n4", podsOnNode4[0].Name)
	assert.Equal(t, "p2n4", podsOnNode4[1].Name)
	assert.Equal(t, "p3n4", podsOnNode4[2].Name)
	assert.Equal(t, "p4n4", podsOnNode4[3].Name)
	assert.Equal(t, "p5n4", podsOnNode4[4].Name)

}

func TestCalculateRequestedCPU(t *testing.T) {
	pods1 := []*apiv1.Pod{
		createTestPod("p1n1", 100),
		createTestPod("p2n1", 300),
	}
	pods2 := []*apiv1.Pod{
		createTestPod("p1n2", 500),
		createTestPod("p2n2", 300),
	}
	pods3 := []*apiv1.Pod{
		createTestPod("p1n3", 500),
		createTestPod("p2n3", 500),
		createTestPod("p3n3", 300),
	}

	pods1Request := calculateRequestedCPU(pods1)
	assert.Equal(t, int64(400), pods1Request)

	pods2Request := calculateRequestedCPU(pods2)
	assert.Equal(t, int64(800), pods2Request)

	pods3Request := calculateRequestedCPU(pods3)
	assert.Equal(t, int64(1300), pods3Request)
}

func TestGetPodCPURequests(t *testing.T) {
	pod1 := createTestPod("pod1", 100)
	pod2 := createTestPod("pod2", 200)

	pod1Request := getPodCPURequests(pod1)
	assert.Equal(t, int64(100), pod1Request)

	pod2Request := getPodCPURequests(pod2)
	assert.Equal(t, int64(200), pod2Request)
}

func TestCopyNodeInfos(t *testing.T) {
	pods1 := []*apiv1.Pod{
		createTestPod("p1n1", 100),
		createTestPod("p2n1", 300),
	}
	pods2 := []*apiv1.Pod{
		createTestPod("p1n2", 500),
		createTestPod("p2n2", 300),
	}
	pods3 := []*apiv1.Pod{
		createTestPod("p1n3", 500),
		createTestPod("p2n3", 500),
		createTestPod("p3n3", 300),
	}

	pod1 := createTestPod("pod1", 200)
	pod2 := createTestPod("pod2", 200)
	pod3 := createTestPod("pod3", 200)

	nodeInfos := NodeInfoArray{
		createTestNodeInfo(createTestNode("node1", 2000), pods1, 400),
		createTestNodeInfo(createTestNode("node2", 2000), pods2, 800),
		createTestNodeInfo(createTestNode("node3", 2000), pods3, 1300),
	}

	// Create a copy of the array
	nodeInfosCopy := nodeInfos.CopyNodeInfos()

	// Modify the array
	nodeInfosCopy[0].AddPod(pod1)
	nodeInfosCopy[1].AddPod(pod2)
	nodeInfosCopy[2].AddPod(pod3)

	// Check the changes applied
	assert.Equal(t, len(pods1)+1, len(nodeInfosCopy[0].Pods))
	assert.Equal(t, len(pods2)+1, len(nodeInfosCopy[1].Pods))
	assert.Equal(t, len(pods3)+1, len(nodeInfosCopy[2].Pods))

	// Check the original has not changed
	assert.Equal(t, len(pods1), len(nodeInfos[0].Pods))
	assert.Equal(t, len(pods2), len(nodeInfos[1].Pods))
	assert.Equal(t, len(pods3), len(nodeInfos[2].Pods))
}

func createTestPod(name string, cpu int64) *apiv1.Pod {
	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      name,
			SelfLink:  fmt.Sprintf("/api/v1/namespaces/default/pods/%s", name),
		},
		Spec: apiv1.PodSpec{
			Containers: []apiv1.Container{
				{
					Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU: *resource.NewMilliQuantity(cpu, resource.DecimalSI),
						},
					},
				},
			},
		},
	}
	return pod
}

func createTestNode(name string, cpu int64) *apiv1.Node {
	node := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: apiv1.NodeStatus{
			Capacity: apiv1.ResourceList{
				apiv1.ResourceCPU:    *resource.NewMilliQuantity(cpu, resource.DecimalSI),
				apiv1.ResourceMemory: *resource.NewQuantity(2*1024*1024*1024, resource.DecimalSI),
				apiv1.ResourcePods:   *resource.NewQuantity(100, resource.DecimalSI),
			},
			Conditions: []apiv1.NodeCondition{
				{
					Type:   apiv1.NodeReady,
					Status: apiv1.ConditionTrue,
				},
			},
		},
	}
	node.Status.Allocatable = node.Status.Capacity
	return node
}

func createTestNodeWithLabel(name string, cpu int64, labels map[string]string) *apiv1.Node {
	node := createTestNode(name, cpu)
	node.ObjectMeta.Labels = labels
	return node
}

func createTestNodeInfo(node *apiv1.Node, pods []*apiv1.Pod, requests int64) *NodeInfo {
	nodeInfo := &NodeInfo{
		Node:         node,
		Pods:         pods,
		RequestedCPU: requests,
		FreeCPU:      node.Status.Capacity.Cpu().MilliValue() - requests,
	}
	return nodeInfo
}

func createFakeClient(t *testing.T) *fake.Clientset {
	pods1 := []apiv1.Pod{
		*createTestPod("p1n1", 100),
		*createTestPod("p2n1", 300),
	}
	pods2 := []apiv1.Pod{
		*createTestPod("p1n2", 500),
		*createTestPod("p2n2", 300),
		*createTestPod("p3n2", 400),
	}
	pods3 := []apiv1.Pod{
		*createTestPod("p1n3", 500),
		*createTestPod("p2n3", 300),
	}
	pods4 := []apiv1.Pod{
		*createTestPod("p1n4", 500),
		*createTestPod("p2n4", 200),
		*createTestPod("p3n4", 400),
		*createTestPod("p4n4", 100),
		*createTestPod("p5n4", 300),
	}

	fakeClient := &fake.Clientset{}
	fakeClient.Fake.AddReactor("list", "pods", func(action core.Action) (bool, runtime.Object, error) {
		listAction, ok := action.(core.ListAction)
		assert.True(t, ok)
		restrictions := listAction.GetListRestrictions().Fields.String()

		podList := &apiv1.PodList{}
		switch restrictions {
		case "spec.nodeName=node1":
			podList.Items = pods1
		case "spec.nodeName=node2":
			podList.Items = pods2
		case "spec.nodeName=node3":
			podList.Items = pods3
		case "spec.nodeName=node4":
			podList.Items = pods4
		default:
			t.Fatalf("unexpected list restrictions: %v", restrictions)
		}
		return true, podList, nil
	})
	return fakeClient
}
