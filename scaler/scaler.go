/*
Copyright 2017 The Kubernetes Authors.
Modifications copyright 2017 Pusher Ltd.

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

package scaler

import (
	"fmt"
	"time"

	"github.com/golang/glog"
	"github.com/pusher/k8s-spot-rescheduler/metrics"
	apiv1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/utils/deletetaint"
	kube_client "k8s.io/client-go/kubernetes"
	kube_record "k8s.io/client-go/tools/record"
)

const (
	// EvictionRetryTime is the time after CA retries failed pod eviction.
	EvictionRetryTime = 10 * time.Second
)

// Originally from https://github.com/kubernetes/autoscaler/blob/bf59e3daa5922c0e44027fa211948b50cb6b7a12/cluster-autoscaler/core/scale_down.go#L690-L723
func evictPod(podToEvict *apiv1.Pod, client kube_client.Interface, recorder kube_record.EventRecorder,
	maxGracefulTerminationSec int, retryUntil time.Time, waitBetweenRetries time.Duration) error {
	recorder.Eventf(podToEvict, apiv1.EventTypeNormal, "Rescheduler", "deleting pod from on-demand node")
	maxGraceful64 := int64(maxGracefulTerminationSec)
	var lastError error
	for first := true; first || time.Now().Before(retryUntil); time.Sleep(waitBetweenRetries) {
		first = false
		eviction := &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: podToEvict.Namespace,
				Name:      podToEvict.Name,
			},
			DeleteOptions: &metav1.DeleteOptions{
				GracePeriodSeconds: &maxGraceful64,
			},
		}
		lastError = client.Core().Pods(podToEvict.Namespace).Evict(eviction)
		if lastError == nil {
			return nil
		}
	}
	glog.Errorf("Failed to evict pod %s, error: %v", podToEvict.Name, lastError)
	recorder.Eventf(podToEvict, apiv1.EventTypeWarning, "ReschedulerFailed", "failed to delete pod from on-demand node")
	return fmt.Errorf("Failed to evict pod %s/%s within allowed timeout (last error: %v)", podToEvict.Namespace, podToEvict.Name, lastError)
}

// DrainNode performs drain logic on the node. Marks the node as unschedulable and later removes all pods, giving
// them up to MaxGracefulTerminationTime to finish.
//
// Originally from https://github.com/kubernetes/autoscaler/blob/bf59e3daa5922c0e44027fa211948b50cb6b7a12/cluster-autoscaler/core/scale_down.go#L725-L783
func DrainNode(node *apiv1.Node, pods []*apiv1.Pod, client kube_client.Interface, recorder kube_record.EventRecorder,
	maxGracefulTerminationSec int, maxPodEvictionTime time.Duration, waitBetweenRetries time.Duration) error {

	drainSuccessful := false
	toEvict := len(pods)
	if err := deletetaint.MarkToBeDeleted(node, client); err != nil {
		recorder.Eventf(node, apiv1.EventTypeWarning, "ReschedulerFailed", "failed to mark the node as draining/unschedulable: %v", err)
		return err
	}

	// If we fail to evict all the pods from the node we want to remove delete taint
	defer func() {
		if !drainSuccessful {
			deletetaint.CleanToBeDeleted(node, client)
			recorder.Eventf(node, apiv1.EventTypeWarning, "ReschedulerFailed", "failed to drain the node, aborting drain.")
		}
	}()

	recorder.Eventf(node, apiv1.EventTypeNormal, "Rescheduler", "marked the node as draining/unschedulable")

	retryUntil := time.Now().Add(maxPodEvictionTime)
	confirmations := make(chan error, toEvict)
	for _, pod := range pods {
		go func(podToEvict *apiv1.Pod) {
			confirmations <- evictPod(podToEvict, client, recorder, maxGracefulTerminationSec, retryUntil, waitBetweenRetries)
		}(pod)
	}

	evictionErrs := make([]error, 0)

	for range pods {
		select {
		case err := <-confirmations:
			if err != nil {
				evictionErrs = append(evictionErrs, err)
			} else {
				metrics.UpdateEvictionsCount()
			}
		case <-time.After(retryUntil.Sub(time.Now()) + 5*time.Second):
			return fmt.Errorf("Failed to drain node %s/%s: timeout when waiting for creating evictions", node.Namespace, node.Name)
		}
	}
	if len(evictionErrs) != 0 {
		return fmt.Errorf("Failed to drain node %s/%s, due to following errors: %v", node.Namespace, node.Name, evictionErrs)
	}

	// Evictions created successfully, wait for the remainder of maxPodEvictionTime to see if pods have been evicted
	var allGone bool
	for time.Now().Before(retryUntil.Add(5 * time.Second)) {
		allGone = true
		for _, pod := range pods {
			podreturned, err := client.Core().Pods(pod.Namespace).Get(pod.Name, metav1.GetOptions{})
			if err == nil {
				glog.Errorf("Not deleted yet %v", podreturned.Name)
				allGone = false
				break
			}
			if !errors.IsNotFound(err) {
				glog.Errorf("Failed to check pod %s/%s: %v", pod.Namespace, pod.Name, err)
				allGone = false
			}
		}
		if allGone {
			glog.V(4).Infof("All pods removed from %s", node.Name)
			// Let the defered function know there is no need for cleanup
			drainSuccessful = true
			recorder.Eventf(node, apiv1.EventTypeNormal, "Rescheduler", "marked the node as drained/schedulable")
			deletetaint.CleanToBeDeleted(node, client)
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("Failed to drain node %s/%s: pods remaining after timeout", node.Namespace, node.Name)
}
