
## Virtual-Kubelet: Triton Provider.
## Mission:
Currently scheduling instances ( Bhyve, KVM, LX, SmartOS ) is very much in the hands of the user.  
 Tooling such as [Terraform](/docs/providers/triton), [Triton-CLI](https://github.com/joyent/node-triton), and [Pulumi](https://pulumi.io/) exist but do not solve the "Auto Scaling Groups" feature set that users are used to using. 
In theory [Kubernetes](http://kubernetes.io) and the [Virtual-Kubelet](https://github.com/virtual-kubelet/virtual-kubelet) Triton Provider will fill this need for a scheduler that runs against the [Triton Cloud-API](https://github.com/joyent/sdc-cloudapi/).

The Primary Focus of this Provider is not to implement the entire Kubernetes EcoSystem, but just the features around managing the lifecycle of instances.  Features outside of this will be considered 2nd class to me, but are definitely welcome if they do not infringe on the primary mission of managing instances.  

I hope in the next few sections of this document to describe the state of what exists, its differences, and the reasons why they differ.


## Pod:
**Kubernetes Defintion**:
A pod (as in a pod of whales or pea pod) is a group of one or more containers (such as Docker containers), with shared storage/network, and a specification for how to run the containers. A pod’s contents are always co-located and co-scheduled, and run in a shared context. A pod models an application-specific “logical host” - it contains one or more application containers which are relatively tightly coupled — in a pre-container world, being executed on the same physical or virtual machine would mean being executed on the same logical host

**Triton Provider Definition**: A pod in the context of the Triton Provider is a Triton Instance and only 1 instance.  This instance as previously stated can be a Hardware Virtual Machine (Bhyve, KVM) , or a Baremetal Container (SmartOS, LX).  The Primary reason this works is heavily due to the Virtualization Primitives that are available for Baremetal Containers on [SmartOS](smartos.org). ** Triton Pods do not support Sidecars**.    In order to use all of the already written [Kubernetes Controllers](https://kubernetes.io/docs/concepts/workloads/controllers/) we use the PodSpec to define the Triton Instances we will be deploying, but only define 1 Container per Pod.   To further understand a "Pod aka Instance"'s lifecycle in Kubernetes please read about [The Lifecycle of a Pod.](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)



### Services, Load Balancing, and Networking
**Problem Statement**: Kubernetes Pods are mortal. They are born and when they die, they are not resurrected. ReplicaSets in particular create and destroy Pods dynamically (e.g. when scaling out or in). While each Pod gets its own IP address, even those IP addresses cannot be relied upon to be stable over time. This leads to a problem: if some set of Pods (let’s call them backends) provides functionality to other Pods (let’s call them frontends) inside the Kubernetes cluster, how do those frontends find out and keep track of which backends are in that set?

** Kubernetes Definitions**:    
- **Services**:  A Kubernetes Service is an abstraction which defines a logical set of Pods and a policy by which to access them - sometimes called a micro-service. The set of Pods targeted by a Service is (usually) determined by a Label Selector (see below for why you might want a Service without a selector)**
- ** VIPs and Service Proxies**: 
Every node in a Kubernetes cluster runs a kube-proxy. kube-proxy is responsible for implementing a form of virtual IP for Services of type other than ExternalName.
In Kubernetes v1.0, Services are a “layer 4” (TCP/UDP over IP) construct, the proxy was purely in userspace. In Kubernetes v1.1, the Ingress API was added (beta) to represent “layer 7”(HTTP) services, iptables proxy was added too, and became the default operating mode since Kubernetes v1.2. In Kubernetes v1.8.0-beta.0, ipvs proxy was added.


** Triton Provider Definition ** :
- ** Sevices**:   Triton Pods (aka instances) do not suffer from the same inability to provision containers directly on different layer 2 fabrics, and layer 3 networks.  Thus do not require the same abstraction in order to route traffic to them.     Load balancing should be handled by the Operator or Application developer at this time and not the scheduler.
-  ** VIPs and Service Proxies**:   As stated in Mission,  "_The Primary Focus of this Provider is not to implement the entire Kubernetes EcoSystem, but just the features around managing the lifecycle of instances. _".  Given that Triton Pods can have interfaces which reside on both an Overlay and ToR (_Top of Rack_)  Network,  Load Balancing and/or Proxying  is simplified and owned by Operator or Application developer not the scheduler. 

To understand the differences I'd suggest reading [Kubernetes Networking](https://kubernetes.io/docs/concepts/services-networking/), and [Triton Networking](https://docs.joyent.com/private-cloud/networks/sdn)  An aside here is that not all Kubernetes plugins behave the same,   At the time of writing this there are over **15** Kubernetes Networking Solutions listed [here](https://kubernetes.io/docs/concepts/cluster-administration/networking/).   Your milage may vary.


### ToDo DNS

### ToDo: Storage 

### ToDo Configuration  & Secrets 

### ToDo Policies


## Getting Started
#### Pre-Requisites: 
- [**Triton Account**](https://www.joyent.com/getting-started)  
- **A Kubernetes Control Plane**:  For this we will use  [Minikube](https://kubernetes.io/docs/setup/minikube/).  In Production use a full fledged [HA Setup of Kubernetes](https://github.com/kelseyhightower/kubernetes-the-hard-way).
- **VK-Triton**

#### Minikube:  
- We will  install Minikube on a Ubuntu 18.04 VM.
``` bash 
### Create Ubuntu 18.04 VM
tt create ac99517a-72ac-44c0-90e6-c7ce3d944a0a 840e0bb3-e7f6-6225-ebe2-c09cf326f0f8 -N My-Fabric-Network -N sdc_nat -n minikube

### SSH Into minikube
triton ssh minikube

### Install Kubectl
sudo apt-get update && sudo apt-get install -y apt-transport-https
curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | sudo apt-key add -
echo "deb https://apt.kubernetes.io/ kubernetes-xenial main" | sudo tee -a /etc/apt/sources.list.d/kubernetes.list
sudo apt-get update
sudo apt-get install -y kubectl

### Install Minikube
curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.30.0/minikube-linux-amd64 && chmod +x minikube && sudo cp minikube /usr/local/bin/ && rm minikube

### Install Docker
sudo apt install docker.io -y && sudo systemctl enable docker && sudo systemctl start docker

### Start Minikube
sudo minikube start --vm-driver=none

### Change kubectl minikube perms
sudo mv /root/.kube $HOME/.kube # this will write over any previous configuration                                                                        
sudo chown -R $USER $HOME/.kube                                                                                                 
sudo chgrp -R $USER $HOME/.kube                                                                                                                                                                                                                                      
sudo mv /root/.minikube $HOME/.minikube # this will write over any previous configuration                                     
sudo chown -R $USER $HOME/.minikube                                                                                             
sudo chgrp -R $USER $HOME/.minikube
```
#### VK-Triton:
- We will run VK-Triton on the Minikube server by changing the Metrics and Kubelet ports.
```  bash
### Curl virtual-kubelet binary that contains the triton-provider 
curl -OL https://github.com/Smithx10/virtual-kubelet/releases/download/alpha/virtual-kubelet-triton-alpha.tar.gz

### Available Sha256
curl -OL https://github.com/Smithx10/virtual-kubelet/releases/download/alpha/virtual-kubelet-triton-alpha.sha256.txt

### Untar
tar zxvf virtual-kubelet-triton-alpha.tar.gz

### The Triton Provider must be able to authenticate with Triton in order to CRUD Instances.  
### Generate an ssh-key
ssh-keygen

### Add the public key to your Triton Account
echo "ssh-rsa my_public_key_here ubuntu@minikube" | triton key add -

### Triton-GO uses ssh-agent in order to use the proper key.  Run the Agent, Add the Key
eval "$(ssh-agent)" && ssh-add

### Export Triton Account Information
export SDC_ACCOUNT=MyAccount
export SDC_KEY_ID=MyKeyID  # KeyID Must be md5 use ssh-keygen -E md5 -lf ~/.ssh/id_rsa
export SDC_URL=https://MyCloudApiURL

###  Create an empty virtual-kubelet provider config
touch triton.toml

### Export the KUBELET_PORT
export KUBELET_PORT=10350

### Run Virtual-Kubelet.  We must specify a non default port for --metrics-addr because minikube uses 10255
./virtual-kubelet --log-level debug --provider triton --provider-config ./triton.toml --nodename vk-triton --metrics-addr :10355 
```
#### Applying a Deployment:
``` bash


cat <<EOF > ./deployment.yml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ubuntu  
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ubuntu
    matchExpressions:
      - {key: app, operator: In, values: [ubuntu]}
  template:
    metadata:
      annotations:
        package: b87c2a29-6b6a-6d19-ea65-aa81e680002c # Triton Package UUID
        networks: "17d7aa50-6ad9-4e56-93b7-12dc868fac2a, a0ae9499-b476-4fef-9861-d0c25029378d" # Triton Package UUIDs
        fwgroup: ubuntu  # Arbitrary Name for the Interal FW Group
        fwenabled: "true" # Enable FW on Deployment
      labels:
        app: ubuntu  # Application Label
        triton.cns.services: ubuntu  # Triton CNS Record
    spec:
      restartPolicy: Always
      containers:
      - image: 7b5981c4-1889-11e7-b4c5-3f3bdfc9b88b  # Triton Image UUID
        name: ubuntu  # Name for the Container
        ports:
          - containerPort: 22  # Published Port.  This will create a Any Firewall rule. Defaults to TCP.
            name: ssh
        env: # Published to mdata.user-data.env_vars
          - name: foo
            value: "bar"
          - name: bar
            value: "baz"
        readinessProbe:   # Will Probe PrimaryIP:22 TCP
          tcpSocket:
            port: 22
          initialDelaySeconds: 10
          periodSeconds: 5
        livenessProbe:	  # Will Probe PrimaryIP:22 TCP
          tcpSocket:
            port: 22
          initialDelaySeconds: 10
          periodSeconds: 5
      nodeSelector:
        kubernetes.io/role: agent
        beta.kubernetes.io/os: linux
        type: virtual-kubelet
      tolerations:
      - key: virtual-kubelet.io/provider
        operator: Exists
EOF

kubectl apply -f ./deployment.yml --record 
kubectl scale --replicas=3 deployment ubuntu
kubectl describe deployment ubuntu
kubectl delete deployment ubuntu
```
