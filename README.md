# Spot Rescheduler

## Introduction

Spot recheduler is a tool that tries to reduce load on a set of Kubernetes nodes. It was designed with the purpose of moving Pods scheduled on AWS on-demand instances to AWS spot instances to allow the on-demand instances to be safely scaled down (By the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)).

The on-demand instances are tainted with the Kubernetes `PreferNoSchedule` taint. This tells the scheduler, if there is space on another node, use that first, if not, use me. But, Kubernetes does not have a way of rescheduling these Pods once they are scheduled on the non-preferred node, which is where the spot-rescheduler comes in.

In reality the rescheduler can be used to remove load from any group of nodes onto a different group of nodes. They just need to be labelled appropriately.

For example, it could also be used to allow controller nodes to take up slack while new nodes are being scaled up, and then rescheduling those pods when the new capacity becomes available, thus reducing the load on the controllers once again.

## Scope
### Does
* Look for Pods on on-demand instances
* Look for space for Pods on spot instances
* Checks the following [predicates](https://github.com/kubernetes/kubernetes/blob/v1.8.0-alpha.3/plugin/pkg/scheduler/algorithm/predicates/predicates.go) when looking for space:
  * CheckNodeMemoryPressure
  * CheckNodeDiskPressure
  * GeneralPredicates
  * MaxAzureDiskVolumeCount
  * MaxGCEPDVolumeCount
  * NoDiskConflict
  * MatchInterPodAffinity
  * PodToleratesNodeTaints
  * MaxEBSVolumeCount
  * NoVolumeZoneConflict
  * ready
* Delete Pods from on-demand instances if there is space free on spot-instances


### Doesn't
* Schedule pods (The default scheduler handles this)
* Scale down empty nodes on your cloud provider (Try the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)))
* Observe PodDisruptionBudgets (On the [TODO](#todo))

## Deployment

There is a basic [deployment](https://github.com/pusher/spot-rescheduler/blob/master/deployment-spot-rescheduler.yaml) file that can be used in the repo.

On this, you should configure the flags as you require.

`--running-in-cluster` (default: `true`): Optional, if this controller is running in a kubernetes cluster, use the
 pod secrets for creating a Kubernetes client.

 `--kube-api-content-type` (default: `application/vnd.kubernetes.protobuf`): Content type of requests sent to apiserver.

`--housekeeping-interval` (default: 10 (seconds)): How often rescheduler takes actions.

`--pod-scheduled-timeout` (default: 120 (seconds): How long should rescheduler should wait for the pod to be rescheduled after evicting it from an on-demand node.

`--listen-address` (default: `localhost:9235`): Address to listen on for serving prometheus metrics

Once this is done you should ensure that you have Kubernetes labels `node-role.kubernetes.io/worker` and `node-role.kubernetes.io/spot-worker` on your on-demand and spot instances respectively and that the on-demand instances are tainted with a `PreferNoSchedule` taint.

For example you could add the following to `ExecStart` in your Kubelet's config file:
```
--register-with-taints="node-role.kubernetes.io/worker=true:PreferNoSchedule"
--node-labels="node-role.kubernetes.io/worker=true"
```

## Operating Logic

The rescheduler logic roughly follows the below:

1. Get a list of Pods scheduled on on-demand nodes
  * List all Pods in all namespaces
  * List all Nodes
  * Filter these lists to return Pods on Nodes that are on-demand
    * Sort on-demand nodes based on least requested CPU first
    * Add the Pods managed by ReplicaSets to the filtered list
2. Iterate through the list of Pods and reschedule them
  * Get a list of spot instance Nodes
    * List all Nodes
    * Filter these based on the spot instance labels
  * Determine if one of the spot nodes has capacity for the pod
    * Sort spot nodes based on most requested CPU first
    * Use scheduler predicates to find a node that the pod would schedule onto
  * Delete the pod on the on-demand instance and wait for the scheduler to schedule it onto a new node (in theory the same one the earlier algorithm chose)
    * Delete pod from on-demand node
    * Wait for the new pod to be scheduled onto a new node

This process is repeated every `housekeeping-interval` seconds.

The effect of this algorithm should be, that we take the emptiest nodes first and empty those before we empty a node which is busier, thus resulting in the highest number of 'empty' nodes that can be removed from the cluster.

## [TODO](#todo)

* Sort pods on worker nodes by most requested CPU first
* Add Prometheus metrics for number of pods on worker nodes and number of pods rescheduled (plus anything else that might be useful)
* Make on-demand and spot instance labels into flags
* Refactor 'worker' to 'onDemand' increase abstraction from Pusher systems
* Look into more mature rescheduling, do we have to 'delete' the Pod? Could try rolling update or blue/green deploys (increase replicas by 1, wait, decrease replicas by 1?)
* Add spacial limits - Don't consider spot instances with less than X% spare resource? Don't consider worker instances with less than X% requested? (Might be cleaned up by autoscaler anyway?)
* Fix `waitForReschedule` method. Doesn't actually work at present, needs to look for new Pod in ReplicaSet.
* Ensure we don't take any action while Pods are Unschedulable - We should let the system stabilise before we start moving things around
* Respect PodDisruptionBudgets
