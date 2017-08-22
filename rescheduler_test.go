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
	"fmt"
	"testing"
	"time"

	simulator "github.com/pusher/spot-rescheduler/predicates"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	core "k8s.io/client-go/testing"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/client/clientset_generated/clientset/fake"
)

func TestWaitForScheduled(t *testing.T) {
	pod := createTestPod("test-pod", 150)
	counter := 0
	fakeClient := &fake.Clientset{}
	fakeClient.Fake.AddReactor("get", "pods", func(action core.Action) (bool, runtime.Object, error) {
		counter++
		if counter > 2 {
			pod.Spec.NodeName = "node1"
		}
		return true, pod, nil
	})

	podsBeingProcessed := NewPodSet()
	podsBeingProcessed.Add(pod)

	assert.True(t, podsBeingProcessed.HasId("kube-system_test-pod"))
	waitForScheduled(fakeClient, podsBeingProcessed, pod)
	assert.False(t, podsBeingProcessed.HasId("kube-system_test-pod"))
}

func TestFindNodeForPod(t *testing.T) {
	predicateChecker := simulator.NewTestPredicateChecker()
	nodes := []*apiv1.Node{
		createTestNode("node1", 500),
		createTestNode("node2", 1000),
		createTestNode("node3", 2000),
	}
	pods1 := []apiv1.Pod{
		*createTestPod("p1n1", 100),
		*createTestPod("p2n1", 300),
	}
	pods2 := []apiv1.Pod{
		*createTestPod("p1n2", 500),
		*createTestPod("p2n2", 300),
	}
	pods3 := []apiv1.Pod{
		*createTestPod("p1n3", 500),
		*createTestPod("p2n3", 500),
		*createTestPod("p3n3", 300),
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
		default:
			t.Fatalf("unexpected list restrictions: %v", restrictions)
		}
		return true, podList, nil
	})

	pod1 := createTestPod("pod1", 100)
	pod2 := createTestPod("pod2", 500)
	pod3 := createTestPod("pod3", 800)
	pod4 := createTestPod("pod4", 2200)

	node := findNodeForPod(fakeClient, predicateChecker, nodes, pod1)
	assert.Equal(t, "node1", node.Name)

	node = findNodeForPod(fakeClient, predicateChecker, nodes, pod2)
	assert.Equal(t, "node2", node.Name)

	node = findNodeForPod(fakeClient, predicateChecker, nodes, pod3)
	assert.Equal(t, "node3", node.Name)

	node = findNodeForPod(fakeClient, predicateChecker, nodes, pod4)
	assert.Nil(t, node)

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

func getStringFromChan(c chan string) string {
	select {
	case val := <-c:
		return val
	case <-time.After(time.Second):
		return "Nothing returned"
	}
}
