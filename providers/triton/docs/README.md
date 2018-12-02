
## Virtual-Kubelet: Triton Provider.
## Mission:
Currently scheduling instances ( Bhyve, KVM, LX, SmartOS ) is very much in the hands of the user.  
 Tooling such as [Terraform](/docs/providers/triton), [Triton-CLI](https://github.com/joyent/node-triton), and [Pulumi](https://pulumi.io/) exist but do not implement the "Auto Scaling Group" feature set that users are familiar with on other platforms. 
In theory [Kubernetes](http://kubernetes.io) and the [Virtual-Kubelet](https://github.com/virtual-kubelet/virtual-kubelet) Triton Provider will fill this need for a scheduler that runs against the [Triton Cloud-API](https://github.com/joyent/sdc-cloudapi/).

The Primary Focus of this Provider is not to implement the entire Kubernetes EcoSystem, but just the features around managing the lifecycle of instances.  Features outside of this will be considered 2nd class, but are definitely welcome if they do not infringe on the primary mission of managing instances.  

I hope in the next few sections of this document to describe the state of what exists, its differences, and the reasons why they differ.


## Pod:
**Kubernetes Defintion**:
A pod (as in a pod of whales or pea pod) is a group of one or more containers (such as Docker containers), with shared storage/network, and a specification for how to run the containers. A pod’s contents are always co-located and co-scheduled, and run in a shared context. A pod models an application-specific “logical host” - it contains one or more application containers which are relatively tightly coupled — in a pre-container world, being executed on the same physical or virtual machine would mean being executed on the same logical host

**Triton Provider Definition**: A pod in the context of the Triton Provider is a Triton Instance and only 1 instance.  This instance as previously stated can be a Hardware Virtual Machine (Bhyve, KVM) , or a Baremetal Container (SmartOS, LX).  The Primary reason this works is heavily due to the Virtualization Primitives that are available for Baremetal Containers on [SmartOS](smartos.org). **Triton Pods do not support Sidecars**.    In order to use all of the already written [Kubernetes Controllers](https://kubernetes.io/docs/concepts/workloads/controllers/) we use the PodSpec to define the Triton Instances we will be deploying, but only define 1 Container per Pod.   To further understand a "Pod aka Instance"'s lifecycle in Kubernetes please read about [The Lifecycle of a Pod.](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)



## Services, Load Balancing, and Networking:
**Problem Statement**: Kubernetes Pods are mortal. They are born and when they die, they are not resurrected. ReplicaSets in particular create and destroy Pods dynamically (e.g. when scaling out or in). While each Pod gets its own IP address, even those IP addresses cannot be relied upon to be stable over time. This leads to a problem: if some set of Pods (let’s call them backends) provides functionality to other Pods (let’s call them frontends) inside the Kubernetes cluster, how do those frontends find out and keep track of which backends are in that set?

**Kubernetes Definitions**:    
- **Services**:  A Kubernetes Service is an abstraction which defines a logical set of Pods and a policy by which to access them - sometimes called a micro-service. The set of Pods targeted by a Service is (usually) determined by a Label Selector (see below for why you might want a Service without a selector)**
- **VIPs and Service Proxies**: 
Every node in a Kubernetes cluster runs a kube-proxy. kube-proxy is responsible for implementing a form of virtual IP for Services of type other than ExternalName.
In Kubernetes v1.0, Services are a “layer 4” (TCP/UDP over IP) construct, the proxy was purely in userspace. In Kubernetes v1.1, the Ingress API was added (beta) to represent “layer 7”(HTTP) services, iptables proxy was added too, and became the default operating mode since Kubernetes v1.2. In Kubernetes v1.8.0-beta.0, ipvs proxy was added.


**Triton Provider Definition** :
- **Sevices**:   Triton Pods (aka instances) do not suffer from the same inability to provision containers directly on different layer 2 fabrics, and layer 3 networks.  Thus do not require the same abstraction in order to route traffic to them.     Load balancing should be handled by the Operator or Application developer at this time and not the scheduler.
-  **VIPs and Service Proxies**:   As stated in [Mission](https://github.com/Smithx10/virtual-kubelet/blob/triton/providers/triton/docs/README.md#mission), _The Primary Focus of this Provider is not to implement the entire Kubernetes EcoSystem, but just the features around managing the lifecycle of instances_. Given that Triton Pods can have interfaces which reside on both an Overlay and ToR (_Top of Rack_)  Network,  Load Balancing and/or Proxying  is simplified and owned by Operator or Application developer not the scheduler. 

To understand the differences I'd suggest reading [Kubernetes Networking](https://kubernetes.io/docs/concepts/services-networking/), and [Triton Networking](https://docs.joyent.com/private-cloud/networks/sdn)  An aside here is that not all Kubernetes plugins behave the same,   At the time of writing this there are over **15** Kubernetes Networking Solutions listed [here](https://kubernetes.io/docs/concepts/cluster-administration/networking/).   Your milage may vary.


## ToDo DNS

## ToDo: Storage 

## ToDo Configuration  & Secrets 

## ToDo Policies




### Getting Started


### Examples:


