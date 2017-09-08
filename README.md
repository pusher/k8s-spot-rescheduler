# Spot Rescheduler

## Introduction

Spot recheduler is a tool that tries to reduce load on a set of Kubernetes nodes. It was designed with the purpose of moving Pods scheduled on AWS on-demand instances to AWS spot instances to allow the on-demand instances to be safely scaled down (By the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)).

Our on-demand instances are tainted with the Kubernetes `PreferNoSchedule` taint. This tells the scheduler, if there is space on another node, use that first. But, Kubernetes does not have a way of rescheduling these Pods once they are scheduled on the non-preferred node, which is where the spot-rescheduler comes in.

In reality the rescheduler can be used to remove load from any group of nodes onto a different group of nodes. They just need to be labelled appropriately.

For example, it could also be used to allow controller nodes to take up slack while new nodes are being scaled up, and then rescheduling those pods when the new capacity becomes available, thus reducing the load on the controllers once again.

This project was inspired by the [Critical Pod Rescheduler](https://github.com/kubernetes/contrib/tree/master/rescheduler) and takes large portions of code from both this repo and the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)).

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
* Builds a plan to move all pods on the an on-demand node to spot nodes
* Empties a node of pods if there is enough space on spot nodes for all of it's pods


### Doesn't
* Schedule pods (The default scheduler handles this)
* Scale down empty nodes on your cloud provider (Try the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)))

## Deployment

There is a basic [deployment](https://github.com/pusher/spot-rescheduler/blob/master/deployment-spot-rescheduler.yaml) file that can be used in the repo.

On this, you should configure the flags as you require.

`--running-in-cluster` (default: `true`): Optional, if this controller is running in a kubernetes cluster, use the
 pod secrets for creating a Kubernetes client.

 `--kube-api-content-type` (default: `application/vnd.kubernetes.protobuf`): Content type of requests sent to apiserver.

`--housekeeping-interval` (default: 10s): How often rescheduler takes actions.

`--node-drain-delay` (default: 10m): How long the scheduler should wait between draining nodes.

`--pod-eviction-timeout` (default: 2m): How long should the rescheduler attempt to retrieve successful pod
 evictions for.

 `--max-graceful-termination` (default: 2m): How long should the rescheduler wait for pods to shutdown gracefully before
  failing the node drain attempt.

`--pod-scheduled-timeout` (default: 2m): How long should rescheduler should wait for the pod to be rescheduled after evicting it from an on-demand node.

`--listen-address` (default: `localhost:9235`): Address to listen on for serving prometheus metrics

Once this is done you should ensure that you have Kubernetes labels `node-role.kubernetes.io/worker` and `node-role.kubernetes.io/spot-worker` (or your own identifiers) on your on-demand and spot instances respectively and that the on-demand instances are tainted with a `PreferNoSchedule` taint.

For example you could add the following to `ExecStart` in your Kubelet's config file:
```
--register-with-taints="node-role.kubernetes.io/worker=true:PreferNoSchedule"
--node-labels="node-role.kubernetes.io/worker=true"
```

## Operating Logic

The rescheduler logic roughly follows the below:

1. Gets a list of on-demand and spot nodes and their respective Pods
  * Builds a map of nodeInfo structs
    * Add node to struct
    * Add pods for that node to struct
    * Add requested and free CPU fields to struct
  * Map these structs based on whether they are on-demand or spot instances.
  * Sort on-demand instances by least requested CPU
  * Sort spot instances by most free CPU
2. Iterate through each on-demand node and try to drain it
  * Iterate through each pod
    * Determine if a spot node has space for the pod
    * Add the pod to the prospective spot node
    * Move onto next node if no spot node space available
  * Drain the node
    * Iterate through pods and evict them in turn
      * Evict pod
      * Wait for deletion and reschedule
    * Cancel all further processing

This process is repeated every `housekeeping-interval` seconds.

The effect of this algorithm should be, that we take the emptiest nodes first and empty those before we empty a node which is busier, thus resulting in the highest number of 'empty' nodes that can be removed from the cluster.

## [TODO](#todo)

* Write unit tests for calculation parts of spot-rescheduler
* Sort out licenses across files
* Add different log levels for more/less verbose logging
