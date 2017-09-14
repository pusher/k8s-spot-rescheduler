/*
Copyright 2017 Pusher Ltd.

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
	"fmt"
	"testing"

	"github.com/pusher/spot-rescheduler/nodes"
	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	simulator "k8s.io/autoscaler/cluster-autoscaler/simulator"
)

func TestFindSpotNodeForPod(t *testing.T) {
	predicateChecker := simulator.NewTestPredicateChecker()

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

	nodeInfos := []*nodes.NodeInfo{
		createTestNodeInfo(createTestNode("node1", 500), pods1, 400),
		createTestNodeInfo(createTestNode("node2", 1000), pods2, 800),
		createTestNodeInfo(createTestNode("node3", 2000), pods3, 1300),
	}

	pod1 := createTestPod("pod1", 100)
	pod2 := createTestPod("pod2", 200)
	pod3 := createTestPod("pod3", 700)
	pod4 := createTestPod("pod4", 2200)

	node := findSpotNodeForPod(predicateChecker, nodeInfos, pod1)
	assert.Equal(t, "node1", node.Node.Name)

	node = findSpotNodeForPod(predicateChecker, nodeInfos, pod2)
	assert.Equal(t, "node2", node.Node.Name)

	node = findSpotNodeForPod(predicateChecker, nodeInfos, pod3)
	assert.Equal(t, "node3", node.Node.Name)

	node = findSpotNodeForPod(predicateChecker, nodeInfos, pod4)
	assert.Nil(t, node)

}

func TestCanDrainNode(t *testing.T) {
	predicateChecker := simulator.NewTestPredicateChecker()

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

	spotNodeInfos := []*nodes.NodeInfo{
		createTestNodeInfo(createTestNode("node3", 2000), pods3, 1300),
		createTestNodeInfo(createTestNode("node2", 1100), pods2, 800),
		createTestNodeInfo(createTestNode("node1", 500), pods1, 400),
	}

	podsForDeletion1 := []*apiv1.Pod{
		createTestPod("pod1", 500),
		createTestPod("pod2", 300),
		createTestPod("pod1", 100),
		createTestPod("pod2", 100),
		createTestPod("pod1", 100),
	}
	podsForDeletion2 := []*apiv1.Pod{
		createTestPod("pod1", 500),
		createTestPod("pod2", 400),
		createTestPod("pod1", 100),
		createTestPod("pod2", 100),
		createTestPod("pod1", 100),
	}

	err1 := canDrainNode(predicateChecker, spotNodeInfos, podsForDeletion1)
	if err1 != nil {
		assert.Fail(t, "canDrainNode should be successful with podsForDeletion1", "%v", err1)
	}

	err2 := canDrainNode(predicateChecker, spotNodeInfos, podsForDeletion2)
	if err2 == nil {
		assert.Fail(t, "canDrainNode should fail with podsForDeletion2, too much requested CPU.")
	}
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

func createTestNodeInfo(node *apiv1.Node, pods []*apiv1.Pod, requests int64) *nodes.NodeInfo {
	nodeInfo := &nodes.NodeInfo{
		Node:         node,
		Pods:         pods,
		RequestedCPU: requests,
		FreeCPU:      node.Status.Capacity.Cpu().MilliValue() - requests,
	}
	return nodeInfo
}
