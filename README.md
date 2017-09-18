# Spot Rescheduler

## Table of contents
* [Introduction](#introduction)
* [Motivation](#motivation)
* [Usage](#usage)
* [Scope of the project](#scope-of-the-project)
* [Operating logic](#operating-logic)
* [Communication](#communication)
* [Contributing](#contributing)
* [License](#license)

## Introduction

Spot rescheduler is a tool that tries to reduce load on a set of Kubernetes nodes. It was designed with the purpose of moving Pods scheduled on AWS on-demand instances to AWS spot instances to allow the on-demand instances to be safely scaled down (By the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)).

In reality the rescheduler can be used to remove load from any group of nodes onto a different group of nodes. They just need to be labelled appropriately.

For example, it could also be used to allow controller nodes to take up slack while new nodes are being scaled up, and then rescheduling those pods when the new capacity becomes available, thus reducing the load on the controllers once again.

This project was inspired by the [Critical Pod Rescheduler](https://github.com/kubernetes/contrib/tree/master/rescheduler) and takes portions of code from both this repo and the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)).

## Motivation

When on-demand instances are tainted with the Kubernetes `PreferNoSchedule` taint, this tells the scheduler, if there is space on another node, use that first. But, Kubernetes does not have a way of rescheduling these Pods once they are scheduled on the non-preferred node, thus leaving you with lots of half filled nodes. When the spot-rescheduler detects unrequested space on spot instances, it moves pods off of on-demand instances and back onto the preferred spot instances, emptying the on-demand instance.

## Usage

### Deploy to Kubernetes
A public docker image is not currently available.

There is a basic [deployment](https://github.com/pusher/spot-rescheduler/blob/master/deployment-spot-rescheduler.yaml) file that can be used in the repo.

On this, you should configure the flags as you require.

### Prebuilt binary
A prebuilt binary is not currently available.

### Building
If you wish to build the binary yourself; first make sure you have go installed and set up. Then clone this repo into your `$GOPATH` and download the dependencies using [`glide`](https://github.com/Masterminds/glide).

```bash
cd $GOPATH/src/github.com # Create this directory if it doesn't exist
git clone git@github.com:pusher/spot-rescheduler <YOUR_FORK>/spot-rescheduler
glide install -v # Installs dependencies to vendor folder.
```

Then build the code using `go build` which will produce the built binary in a file `spot-rescheduler`.

### Flags
`-v` (default: 0): The log verbosity level the program should run in, currently numeric with values between 2 & 4, recommended to use `-v=2`

`--running-in-cluster` (default: `true`): Optional, if this controller is running in a kubernetes cluster, use the pod secrets for creating a Kubernetes client.

`--namespace` (deafult: `kube-system`): Namespace in which spot-rescheduler is run.

 `--kube-api-content-type` (default: `application/vnd.kubernetes.protobuf`): Content type of requests sent to apiserver.

`--housekeeping-interval` (default: 10s): How often rescheduler takes actions.

`--node-drain-delay` (default: 10m): How long the scheduler should wait between draining nodes.

`--pod-eviction-timeout` (default: 2m): How long should the rescheduler attempt to retrieve successful pod evictions for.

 `--max-graceful-termination` (default: 2m): How long should the rescheduler wait for pods to shutdown gracefully before failing the node drain attempt.

`--listen-address` (default: `localhost:9235`): Address to listen on for serving prometheus metrics.

`--on-demand-node-label` (default: `node-role.kubernetes.io/worker`) Name of label on nodes to be considered for draining.

`--spot-node-label` (default: `node-role.kubernetes.io/spot-worker`) Name of label on nodes to be considered as targets for pods.

Once this is done you should ensure that you have Kubernetes labels `node-role.kubernetes.io/worker` and `node-role.kubernetes.io/spot-worker` (or your own identifiers) on your on-demand and spot instances respectively and that the on-demand instances are tainted with a `PreferNoSchedule` taint.

For example you could add the following flags to your Kubelet:
```
--register-with-taints="node-role.kubernetes.io/worker=true:PreferNoSchedule"
--node-labels="node-role.kubernetes.io/worker=true"
```

## Scope of the project
### Does
* Look for Pods on on-demand instances
* Look for space for Pods on spot instances
* Checks the following [predicates](https://github.com/kubernetes/kubernetes/blob/v1.8.0-alpha.3/plugin/pkg/scheduler/algorithm/predicates/predicates.go) when determining whether a pod can be moved:
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
* Checks whether there is enough capacity to move all pods on the on-demand node to spot nodes
* Evicts all pods on the node if the previous check passes
* Leaves the node in a schedulable state - in case it's capacity is required again


### Does not
* Schedule pods (The default scheduler handles this)
* Scale down empty nodes on your cloud provider (Try the [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)))

## Operating logic

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

## Communication

* Found a bug? Please open an issue.
* Have a feature request. Please open an issue.
* If you want to contribute, please submit a pull request

## Contributing
To develop on this project, clone this repo into your `$GOPATH` and download the dependencies using [`glide`](https://github.com/Masterminds/glide).

```bash
cd $GOPATH/src/github.com # Create this directory if it doesn't exist
git clone git@github.com:<YOUR_FORK>/spot-rescheduler <YOUR_FORK>/spot-rescheduler
glide install -v # Installs dependencies to vendor folder.
```

The main package is within `rescheduler.go` and an overview of it's operating logic is described [above](#operating-logic).

If you want to run the rescheduler locally you must have a valid `kubeconfig` file in your repo root and then run the program with the flag `--running-in-cluster=false`.

### Tests
Unit tests are covering the decision making parts of this code and can be run using the build in Go test suite.

To run the tests: `go test $(glide novendor)`

## License
This project is licensed under Apache 2.0 and a copy of the license is available [here](https://github.com/pusher/spot-rescheduler/blob/master/license).
