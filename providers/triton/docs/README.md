
## Virtual-Kubelet: Triton Provider.
## Mission:
Currently scheduling instances ( Bhyve, KVM, LX, SmartOS ) on Triton is very much in the hands of the user.  
 Tooling such as [Terraform](/docs/providers/triton), [Triton-CLI](https://github.com/joyent/node-triton), and [Pulumi](https://pulumi.io/) exist but do not implement "Auto Scaling Groups".  In theory [Kubernetes](http://kubernetes.io) and the [Virtual-Kubelet](https://github.com/virtual-kubelet/virtual-kubelet) Triton Provider will fill this need for a scheduler that runs against the [Triton Cloud-API](https://github.com/joyent/sdc-cloudapi/).

The Primary Focus of this Provider is not to implement the entire Kubernetes EcoSystem, but just the features around managing the lifecycle of instances.  Features outside of this will be considered 2nd class, but are definitely welcome if they do not infringe on the primary mission of managing instances.  

I hope in the next few sections of this document to describe the state of what exists, its differences, and the reasons why they differ.


## Pod:
**Kubernetes Defintion**:
A pod (as in a pod of whales or pea pod) is a group of one or more containers (such as Docker containers), with shared storage/network, and a specification for how to run the containers. A pod’s contents are always co-located and co-scheduled, and run in a shared context. A pod models an application-specific “logical host” - it contains one or more application containers which are relatively tightly coupled — in a pre-container world, being executed on the same physical or virtual machine would mean being executed on the same logical host

**Triton Provider Definition**: A pod in the context of the Triton Provider is a Triton Instance and only 1 instance.  This instance as previously stated can be a Hardware Virtual Machine (Bhyve, KVM) , or a Baremetal Container (SmartOS, LX).  The Primary reason this works is heavily due to the Virtualization Primitives that are available for Baremetal Containers on [SmartOS](smartos.org). **Triton Pods do not support Sidecars**.  I encourage you to run as many processes as you want.  In order to use all of the already written [Kubernetes Controllers](https://kubernetes.io/docs/concepts/workloads/controllers/) we use the PodSpec to define the Triton Instances we will be deploying, but only define 1 Container per Pod.   To further understand a "Pod's"(Instance) lifecycle in Kubernetes please read about [The Lifecycle of a Pod.](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)

_Note_: You can ssh into your "Pod".


### Services, Load Balancing, and Networking
**Problem Statement**: Kubernetes Pods are mortal. They are born and when they die, they are not resurrected. ReplicaSets in particular create and destroy Pods dynamically (e.g. when scaling out or in). While each Pod gets its own IP address, even those IP addresses cannot be relied upon to be stable over time. This leads to a problem: if some set of Pods (let’s call them backends) provides functionality to other Pods (let’s call them frontends) inside the Kubernetes cluster, how do those frontends find out and keep track of which backends are in that set?

**Kubernetes Definitions**:    
- **Services**:  A Kubernetes Service is an abstraction which defines a logical set of Pods and a policy by which to access them - sometimes called a micro-service. The set of Pods targeted by a Service is (usually) determined by a Label Selector (see below for why you might want a Service without a selector)**
- **VIPs and Service Proxies**: 
Every node in a Kubernetes cluster runs a kube-proxy. kube-proxy is responsible for implementing a form of virtual IP for Services of type other than ExternalName.
In Kubernetes v1.0, Services are a “layer 4” (TCP/UDP over IP) construct, the proxy was purely in userspace. In Kubernetes v1.1, the Ingress API was added (beta) to represent “layer 7”(HTTP) services, iptables proxy was added too, and became the default operating mode since Kubernetes v1.2. In Kubernetes v1.8.0-beta.0, ipvs proxy was added.


**Triton Provider Definitions** :
- **Sevices**:   Triton Pods (aka instances) do not suffer from the same inability to provision containers directly on different layer 2 fabrics, and layer 3 networks.  Thus do not require the same abstraction in order to route traffic to them.     Load balancing should be handled by the Operator or Application developer at this time and not the scheduler.
-  **VIPs and Service Proxies**:   As stated in Mission,  "_The Primary Focus of this Provider is not to implement the entire Kubernetes EcoSystem, but just the features around managing the lifecycle of instances_".  Given that Triton Pods can have interfaces which reside on both an Overlay and ToR (_Top of Rack_)  Network,  Load Balancing and/or Proxying  is simplified. Ownership is moved from the scheduler to the Operator/Developer.

To understand the differences I'd suggest reading [Kubernetes Networking](https://kubernetes.io/docs/concepts/services-networking/), and [Triton Networking](https://docs.joyent.com/private-cloud/networks/sdn)  An aside here is that not all Kubernetes plugins behave the same,   At the time of writing this there are over **15** Kubernetes Networking Solutions listed [here](https://kubernetes.io/docs/concepts/cluster-administration/networking/).   Your milage may vary.


## Getting Started
#### Pre-Requisites: 
- [**Triton Account**](https://www.joyent.com/getting-started)  
- **A Kubernetes Control Plane**:  For this we will use  [Minikube](https://kubernetes.io/docs/setup/minikube/).  In Production use a full fledged [HA Setup of Kubernetes](https://github.com/kelseyhightower/kubernetes-the-hard-way).
- **VK-Triton**

#### Minikube:  
- We will install Minikube on a Ubuntu 18.04 VM.
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
        affinity: container==bastion
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


## Creating a Triton Kubernetes Deployment

**Deploying an Instance on Triton involves the following attributes**
  - Name
  - Package
  - Image
  - Networks
  - Affinity Rules
  - Metadata
  - Tags
  - Ports
  - FWEnabled
  - FWGroup
  - DelProtect

As stated earlier,  we must fit the normal "triton instance create" input parameters into the PodSec that kubernetes uses.  Luckily for us, Triton only has a few outliars that don't map 1 to 1.   In this situation we just add them as "annotations" in metadata.annotations.


|  Triton | PodSpec | Type  | Example |
|---|---|---|---|---|
|  instance.Name | spec.containers.name|string | "consul" 
|instance.Package| metadata.annotations.package |string | "9cd95f47-11ea-4f61-c323-fe526fc1c544" 
|instance.Image | spec.containers.image | string | "c239a01f-5fe9-456f-8f92-53f5b7efd53d"
|instance.Networks | metadata.annotations.networks | string (csv) | "913849ab-17c4-4a03-9c2d-1147b8bf4d24, 913849ab-17c4-4a03-9c2d-1147b8bf4d24"
|instance.Affinity | metadata.annotations.affinity| string| "role!=consul"
|instance.Metadata | metadata.annotations | KV | foo: "bar"
|instance.Tags | metadata.labels | KV | triton.cns.services: "consul"
|instance.Ports | spec.containers.port | [ContainerPort](https://godoc.org/k8s.io/api/core/v1#ContainerPort) | ports: [{ "name": "ssh", "containerPort": 22 }]
|instance.FWEnabled | metadata.annotations.fwenabled | bool | true
|instance.FWGroup | metadata.annotations.fwgroup | string | "consul"
|instance.DelProtect | metadata.annotations.delprotect | bool | false
   
 ``` yml
apiVersion: apps/v1                                                                                                                                                                                                
kind: Deployment                                                                                                                                                                                                   
metadata:                                                                                                                                                                                                          
  name: consul                                                                                                                                                                                                     
spec:                                                                                                                                                                                                              
  replicas: 1                                                                                                                                                                                                      
  selector:                                                                                                                                                                                                        
    matchLabels:                                                                                                                                                                                                   
      app: consul                                                                                                                                                                                                  
    matchExpressions:                                                                                                                                                                                              
      - {key: app, operator: In, values: [consul]}                                                                                                                                                                 
  template:                                                                                                                                                                                                        
    metadata:                                                                                                                                                                                                      
      annotations:                                                                                                                                                                                                 
        package: "9cd95f47-11ea-4f61-c323-fe526fc1c544"                                                                                                                                                            
        networks: "913849ab-17c4-4a03-9c2d-1147b8bf4d24, 17d7aa50-6ad9-4e56-93b7-12dc868fac2a"                                                                                                                                                           
        fwgroup: consul                                                                                                                                                                                            
        fwenabled: "true"
        affinity: "role!=consul"
        delprotect: "true"
        foo: "bar"
        bar: "baz"                                                                                                                                                                                          
      labels:                                                                                                                                                                                                      
        app: consul                                                                                                                                                                                                
        triton.cns.services: consul                                                                                                                                                                                
    spec:                                                                                                                                                                                                          
      restartPolicy: Always                                                                                                                                                                                        
      containers:                                                                                                                                                                                                  
      - image: c239a01f-5fe9-456f-8f92-53f5b7efd53d                                                                                                                                                                
        name: consul                                                                                                                                                                                               
        ports:                                                                                                                                                                                                     
          - containerPort: 8500                                                                                                                                                                                    
            protocol: TCP                                                                                                                                                                                          
            name: consul                                                                                                                                                                                           
          - containerPort: 22                                                                                                                                                                                      
            name: ssh                                                                                                                                                                                              
        envFrom:                                                                                                                                                                                                   
        - configMapRef:                                                                                                                                                                                            
            name: consul-config                                                                                                                                                                                    
        env:                                                                                                                                                                                                       
          - name: SECRET_USERNAME                                                                                                                                                                                  
            valueFrom:                                                                                                                                                                                             
              secretKeyRef:                                                                                                                                                                                        
                name: bob                                                                                                                                                                                          
                key: username                                                                                                                                                                                      
          - name: SECRET_PASSWORD                                                                                                                                                                                  
            valueFrom:                                                                                                                                                                                             
              secretKeyRef:                                                                                                                                                                                        
                name: bob                                                                                                                                                                                          
                key: password                                                                                                                                                                                      
          - name: FOO                                                                                                                                                                                              
            valueFrom:                                                                                                                                                                                             
              configMapKeyRef:                                                                                                                                                                                     
                name: consul-config                                                                                                                                                                                
                key: foo                                                                                                                                                                                           
          - name: CONFIG_VAR                                                                                                                                                                                       
            valueFrom:                                                                                                                                                                                             
              configMapKeyRef:                                                                                                                                                                                     
                name: consul-config                                                                                                                                                                                
                key: consul-config                                                                                                                                                                                 
          - name: CONTAINERPILOT                                                                                                                                                                                   
            value: "/etc/containerpilot.json5"                                                                                                                                                                     
          - name: CONSUL_AGENT                                                                                                                                                                                     
            value: "1"                                                                                                                                                                                             
          - name: CONSUL_BOOTSTRAP_EXPECT                                                                                                                                                                          
            value: "5"                                                                                                                                                                                             
          - name: CONSUL                                                                                                                                                                                           
            value: "consul-demo.consul.svc.smith.us-east-1.cns.mydomain.us"                                                                                                                                      
          - name: NODE_EXPORTER                                                                                                                                                                                    
            value: "1"                                                                                                                                                                                             
        readinessProbe:                                                                                                                                                                                            
          httpGet:                                                                                                                                                                                                 
            path: /ui                                                                                                                                                                                              
            port: 8500                                                                                                                                                                                             
          initialDelaySeconds: 3                                                                                                                                                                                   
          periodSeconds: 2                                                                                                                                                                                         
        livenessProbe:                                                                                                                                                                                             
          httpGet:                                                                                                                                                                                                 
            path: /ui                                                                                                                                                                                              
            port: 8500                                                                                                                                                                                             
          initialDelaySeconds: 3                                                                                                                                                                                   
          periodSeconds: 2                                                                                                                                                                                         
        #livenessProbe:                                                                                                                                                                                            
          #tcpSocket:                                                                                                                                                                                              
            #port: 22                                                                                                                                                                                              
          #initialDelaySeconds: 10                                                                                                                                                                                 
          #periodSeconds: 5                                                                                                                                                                                        
      nodeSelector:                                                                                                                                                                                                
        kubernetes.io/role: agent                                                                                                                                                                                  
        beta.kubernetes.io/os: linux                                                                                                                                                                               
        type: virtual-kubelet                                                                                                                                                                                      
      tolerations:                                                                                                                                                                                                 
      - key: virtual-kubelet.io/provider                                                                                                                                                                           
        operator: Exists   
```

This manifest results in the following provisioned instance:
``` json
{
    "id": "5213157a-fcc7-439f-bd3b-eb34dbeff3b3",
    "name": "consul-67f68648fc-xb9wj",
    "type": "smartmachine",
    "brand": "lx",
    "state": "running",
    "image": "c239a01f-5fe9-456f-8f92-53f5b7efd53d",
    "ips": [
        "10.0.0.228",
        "10.1.10.206"
    ],
    "memory": 2048,
    "disk": 20480,
    "deletion_protection": true,
    "metadata": {
        "bar": "baz",
        "foo": "bar",
        "k8s_pod": "{\"metadata\":{\"name\":\"consul-67f68648fc-xb9wj\",\"generateName\":\"consul-67f68648fc-\",\"namespace\":\"default\",\"selfLink\":\"/api/v1/namespaces/default/pods/consul-67f68648fc-xb9wj\",\"uid\":\"88db751b-f75e-11e8-96a5-90b8d0b8add9\",\"resourceVersion\":\"1091572\",\"creationTimestamp\":\"2018-12-04T00:50:02Z\",\"labels\":{\"app\":\"consul\",\"pod-template-hash\":\"67f68648fc\",\"triton.cns.services\":\"consul\"},\"annotations\":{\"affinity\":\"role!=consul\",\"bar\":\"baz\",\"foo\":\"bar\",\"fwenabled\":\"true\",\"fwgroup\":\"consul\",\"networks\":\"913849ab-17c4-4a03-9c2d-1147b8bf4d24, 17d7aa50-6ad9-4e56-93b7-12dc868fac2a\",\"package\":\"9cd95f47-11ea-4f61-c323-fe526fc1c544\"},\"ownerReferences\":[{\"apiVersion\":\"apps/v1\",\"kind\":\"ReplicaSet\",\"name\":\"consul-67f68648fc\",\"uid\":\"88ce32ad-f75e-11e8-96a5-90b8d0b8add9\",\"controller\":true,\"blockOwnerDeletion\":true}]},\"spec\":{\"volumes\":[{\"name\":\"default-token-n5zfl\",\"secret\":{\"secretName\":\"default-token-n5zfl\",\"defaultMode\":420}}],\"containers\":[{\"name\":\"consul\",\"image\":\"c239a01f-5fe9-456f-8f92-53f5b7efd53d\",\"ports\":[{\"name\":\"consul\",\"containerPort\":8500,\"protocol\":\"TCP\"},{\"name\":\"ssh\",\"containerPort\":22,\"protocol\":\"TCP\"}],\"envFrom\":[{\"configMapRef\":{\"name\":\"consul-config\"}}],\"env\":[{\"name\":\"SECRET_USERNAME\",\"value\":\"admin\",\"valueFrom\":{\"secretKeyRef\":{\"name\":\"bob\",\"key\":\"username\"}}},{\"name\":\"SECRET_PASSWORD\",\"value\":\"1f2d1e2e67df\",\"valueFrom\":{\"secretKeyRef\":{\"name\":\"bob\",\"key\":\"password\"}}},{\"name\":\"FOO\",\"value\":\"bar\",\"valueFrom\":{\"configMapKeyRef\":{\"name\":\"consul-config\",\"key\":\"foo\"}}},{\"name\":\"CONFIG_VAR\",\"value\":\"datacenter mydatacenter\\nfoo bar\\nbar baz\\nbaz foo\\n\",\"valueFrom\":{\"configMapKeyRef\":{\"name\":\"consul-config\",\"key\":\"consul-config\"}}},{\"name\":\"CONTAINERPILOT\",\"value\":\"/etc/containerpilot.json5\"},{\"name\":\"CONSUL_AGENT\",\"value\":\"1\"},{\"name\":\"CONSUL_BOOTSTRAP_EXPECT\",\"value\":\"5\"},{\"name\":\"CONSUL\",\"value\":\"consul-demo.consul.svc.smith.us-east-1.cns.mydomain\"},{\"name\":\"NODE_EXPORTER\",\"value\":\"1\"}],\"resources\":{},\"volumeMounts\":[{\"name\":\"default-token-n5zfl\",\"readOnly\":true,\"mountPath\":\"/var/run/secrets/kubernetes.io/serviceaccount\"}],\"livenessProbe\":{\"httpGet\":{\"path\":\"/ui\",\"port\":8500,\"scheme\":\"HTTP\"},\"initialDelaySeconds\":10,\"timeoutSeconds\":1,\"periodSeconds\":5,\"successThreshold\":1,\"failureThreshold\":3},\"readinessProbe\":{\"httpGet\":{\"path\":\"/ui\",\"port\":8500,\"scheme\":\"HTTP\"},\"initialDelaySeconds\":10,\"timeoutSeconds\":1,\"periodSeconds\":5,\"successThreshold\":1,\"failureThreshold\":3},\"terminationMessagePath\":\"/dev/termination-log\",\"terminationMessagePolicy\":\"File\",\"imagePullPolicy\":\"Always\"}],\"restartPolicy\":\"Always\",\"terminationGracePeriodSeconds\":30,\"dnsPolicy\":\"ClusterFirst\",\"nodeSelector\":{\"beta.kubernetes.io/os\":\"linux\",\"kubernetes.io/role\":\"agent\",\"type\":\"virtual-kubelet\"},\"serviceAccountName\":\"default\",\"serviceAccount\":\"default\",\"nodeName\":\"triton-vk\",\"securityContext\":{},\"schedulerName\":\"default-scheduler\",\"tolerations\":[{\"key\":\"virtual-kubelet.io/provider\",\"operator\":\"Exists\"},{\"key\":\"node.kubernetes.io/not-ready\",\"operator\":\"Exists\",\"effect\":\"NoExecute\",\"tolerationSeconds\":300},{\"key\":\"node.kubernetes.io/unreachable\",\"operator\":\"Exists\",\"effect\":\"NoExecute\",\"tolerationSeconds\":300}],\"priority\":0},\"status\":{\"phase\":\"Pending\",\"conditions\":[{\"type\":\"PodScheduled\",\"status\":\"True\",\"lastProbeTime\":null,\"lastTransitionTime\":\"2018-12-04T00:50:03Z\"}],\"qosClass\":\"BestEffort\"}}",
        "user-data": "{\"env_vars\": {\"CONFIG_VAR\":\"datacenter mydatacenter\\nfoo bar\\nbar baz\\nbaz foo\\n\",\"CONSUL\":\"consul-demo.consul.svc.smith.us-east-1.cns.mydomain\",\"CONSUL_AGENT\":\"1\",\"CONSUL_BOOTSTRAP_EXPECT\":\"5\",\"CONTAINERPILOT\":\"/etc/containerpilot.json5\",\"FOO\":\"bar\",\"NODE_EXPORTER\":\"1\",\"SECRET_PASSWORD\":\"1f2d1e2e67df\",\"SECRET_USERNAME\":\"admin\",\"consul-config\":\"datacenter mydatacenter\\nfoo bar\\nbar baz\\nbaz foo\\n\",\"foo\":\"bar\"}}",
        "root_authorized_keys": "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDU6mGTN3ztmIwgd5sZt6AhXH7PidRc+yZ9OospfgBA7cRQaFoWavYcdw5F0vOEzt0USrlPZjxKPxOX2eoF98os3A3H4fp6+5LCkNnn+OZcbpkbf+53j0pNHvfH9X7FiyVez4F8v7uC7KWiBDKy1J3OB026bgMnpV3+PtKiC3zG0BQGcf/KN+QRZqk9qAAdEbSSUHtc+1wJEZDWVTjNREQGzZVn5F1pm4YkQz44WnD6wndsLJ9+e5vEscON3SlUujJPoOGKBu+uuxhjS5kPR5+hMJ3fjtfGCWYudWIE5ZLQI1LrD/7qfHDKtUyKD0eLSPtwuhFk7zJYuao6zKbZaEH/ bruce.smith@Bruces-MacBook-Pro.local\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDPdNLwZ0Rxu4OtnyAmqnK0tgTftPWGdYoqZsPPuNOHVUcFXdkilIbNBif6ldQKBkDzv0Cnu9Jd1RkTPV4nQQbi7P04yGGYaZt4YYjQgWjRq+lEZgN/KP7gBmRg+KueVWTxELfzRk+f6y4h72UNgFzcC9w0sBsUDPbnyMNCSBaGCoAWjNYMJMshTEh3m66mW13AG40UhP4ZSddbpPSc5RBuzMpYUapD984Z/Hx3qrYFD1d5j5pD4z2HlztxbOZyFt8j+SEEdiFoHDp2Ubfa1NgPT4JYgJZmCE2ol/yPHVwwibbP+Y3TX9PxfPSpbVyK9EUfPlRQ0zeJ5t2LFeZJZnxT root@DESKTOP-0KROB1I\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCS/cMV5KjxSPYU1rQl5qhPDODIbkMMxWBaDrvn2c+zwdzUcUMV+KgdDryj6LTKbdCeGHPYRk+z8KSrhctE7bG7Nw+ADl9DXOZ4nGapKh3KBDX9jWGm/+8qgXbNCJ8zQnJ3XPt9our6Qwfge7vxRIMYPevgl0U23zTPaeuv+yelU4+m6Hkv4IiHpcrL/Mr6rEI3W+mt6WaZZsjnldChXj9hfE0FzIYqntTT31jo97W2dc9n6nLFZWlR4AViyzBWJHrzWscbiZBbbRQQHf9Lz7UUh3alJYTiVsrdnTkp6VSBXOy0022aor3Bz7qOZS454A0CLY3VcOJGaaJuJx9wN/hP root@1a8cd51d-2cbd-406f-c576-9d9c7a58a106\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQCuV6gfpf8ATLii/zbG1kODITDWHw49GHR09Np8tKiLGLDwjHFVVJpsznsqX+268P+6vbgjJVmZiPV1cuwfTSWLEWJMcb711bvIG7D6RWStTSFEej+PX7CM+J2TKbUho3kbhv+HAGsBV1vKJhV6EG7A8onIMKwA3K9griZbCPUy6eFKnm/hsFhIjCdQh3aJtscskSgZXqtBZPBg5PSC/rI5vhgRLDUXB9eyjwv9yg2KVONGeui1+8chg+00DDPzqiDMYxofED1rKnNLu6uy5fBHl3oY8wzDOzLxxZuMAcCmBiYTJWrGCPKAICJ15dBaUesxvdVA9MgguVmkfuVxnbV4D/+2nvM2qCr84A2PlZLRqSeernq18avPUeQTXLuLhJ7DRTeFHCr0xJoSEvoW21eTg1+cUQFTfu3RywEGCyKEyfvPc2dqjuqvXrUQ9+ryKGekAI+ZWYYUZK2uri5hkTv1nQOSF0n2IMI+3zU8S9eDhM1UFgLbGcZ1Oeq3MBo4F6hFCjUJLPiDiD7XEJCCacOuio3kNH4yMXLF53xQLDj2e4pjGpW3RcI4sCUv/Ne+BvrSJ4zG5xPzN1nNuY5u7cSq2P6GaSMFQtLvSzViqOR8a+hZMGHc9C53KaK7BoPZ/wUn+ajZdXziQ0gihjJNeCrE7NntsHTwVK59EDZzE9J0yw== brad.miller@us.imshealth.com\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDe8lAopBHnYkrfAD0ZpvyZGe+NWUgbiGkC9rKG1htXqtr/UOPvlooSe4DcCOmUgwNpPEd4ArlTAbAfwyNR5hOasjJKdvekku01coVCNVQwE97a50r1ovaQ0Sq4c+PWt46SYYoekUib72cPdFG/eADmXGU0OWNL3M4TRwODbI6bexXPfzHqB/Dv3nDfV8NdZqyNiEux7QKA50khdFyLdGGchoqJLkirQ7ctacBDfSGb4rsVPa3IGxZIxOUcLhK5fZJ1v9L0+LhAExa2Rnf602DcCM1qysnYoobi/K5L0yy+iUUqSiPrskPrXOn256lI8nI9RAzfFw56eH14fvhhUDvj root@44528303-2479-62aa-c264-c9461d7f4d51\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDFUFFFiZei8znyS7X0bmkp57hBKqotQQ7NdJXRXX8Yr+LIxa8H41jXDaAy6SIhjLx4u3ILiXaLJU/dHnWtFJmYaGkF6dJPzjlkwWrxtO9hzmAV02yK34Oppsw1Uz0vw0h5dVeGIHJGJj1MyptfeK0k9AZsobVkLVZL+KvP1W9OqrQq1FmE4oVSaQbce8yJ9jHpPCWvl4Z/apT4l78W8hubgjw90/UgSHdlSRJge3b9IysuSJbtBIp2bgHC4fAzZPsSvrIGHrLfnNkwoYddIfVh3hV2dyZv8UcAYcPiWuayoTaagjhybiL67+H+/fwTEIfLsoMbEQGKIGACXwX157T3 soumyanair@Soumyas-MacBook-Pro.local\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQDql2OPfxI1v1rVuAh2uuCFoMIcJroDwh1qQJ1xWF7CMgxEEV53kDs5Mw73GUIaXlUYNo80Zw745tX1oWNFuBbbWmKgEUACK3SXviHHjPopMnvv9rA4Ie5of1Q41x3OPoREfc7EJk/U9u0y7AALEBE7cT4ZTzJIc3fd7kdHnGF9Ww1U2kt+FXldrme8KSaMWoGlxVWKnob96Z9z6dw9Lm4FC2R7iFfaVa+z5qOhIrZZR+LJCv1xpJEfiPioqRvltWdYbiJaqOCm0NQO+RCQHhXxs6AcZxS84gBlHoagDsEo3xUIGIyTKA7AFVDrSBkeTFNjdWcBqLYSOJZNQbBnwFD/t0B1xiyeZkSf+H1dOwED0Q8m7XpA0NMhekBWpmodSIaS7JqKkYO2lFJWyNif/2v64d42+GAiw6tiuaInCdFtMx8ePZLTp6nL9qCDrBd767E9vLaHFEcLLIuS3sUsr+ddcomB1j2Uoo1zn9eO9oF3WAmj+QqlLezQsZU6+JzeuNEkavaGxoBICBgQYF3RzcS7dPnOFilfxNho7VpWDFSevh3ONivA4RAUidaZjkoQ1LJ/EwLvoq4tU/EaAWAtQQRMgHRo1ERD8E9qNtshsLIpz7XtNDzltp9Rf2gOCqKCiuNhYqNBQNsrRDlz+qAXV4BEn+YrYaZQZhZjiEG4ZE++vw==\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCh4GF7zymaH3PQhFoKlW76wczYfRwerWMoDIh2yM6PJOsryO4uU69PgbOcXrBzqBsh2PcCu1zJc+/B8fmvoFEbDLHUlRAQZQI/mCnaji5oMUFs24KryyYY5lT24z4cB5Df6yOMYLrEp83LgHKyr8Acevt5M+dYgXfPkE8t1Nv95sV29GeGq7AgqLE5sgkNTt70Vv9bqzgEdRtwHV49T9f/IRZ2tIWj9yYqdenq9hAe9/3Dw0ZEuw0PZavLCfKxbJx0341Dyqgzdt7OF3NvM2aK3hkOyFimaNgPjfFGAqfdl4uhh7Cku6HEov0Y6eUSYfMbinfxK9T5zpWFRvIJkMj3 smith@arch-nix\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCUbBXv3UCyQFvTXhwoI2cHNFRcmcJVCT33mkgc5fok7weFj8ZXtKSYLWdoMKT04uliiH2mk+EYktJcyUEoOClzbIDwY76bKiaQJYu+6vBZg4lumWLsdmaG1Wgf1aLpU2Ghm6HS/opE12VASXIVsgh4WqEx7FLGvX3doym88JVthaZvEYQza2H3dSvAKG3gi8ySsGYJ027b4RlNlfjTklx0FYfCPqcPTGssgCsO5aF9SzhBczJzBAsMoxgAjE0BFAPZnr3noXKXK4FaxDFB/BP2+lzKX6tYkvgnZwoHayEyGBnQp9dlN8WWbdSzyXQZyRUxVliPjAfkVL16ChUZ+5/F root@vkube\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDQNS3+/ZAt0m7lDTUY9V2hiX1t016j7YAWBqSAE9vxV2dbcUknS7OiN274fqn5fCCJyOIoQpd8OMKhCaglI3QqR8t5fNEsziXT+5Qt3fVUOY5LIdoxuhZpM4l3PmT7dHgNF5wwVCfKvUkHEUpqLqkSbbBfq1eqWsTX4+MzS5cTSNwXE1v+TxNV2Sk6UGjq4f5ovs17bMPtGgAmEGqcYCgBZcoK+iDoLp4FUDkfH2DDsp9pR2XfaOYX2rtIyrTtAKrMZObM7KCrDsDgIvaxeZaVwqdqLlOGf6JrbPzBdHl/EutDtQQQtFzYG6RBszLVKCWHBbUEsoImia62oIAt9v6j root@6f325118-7479-e1f5-fca0-d2a4a293dadb\nssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC2dtVCfsYhsTJeP1jKr51JMiX47jTsTf+WtE60sIGNtDZQgtHKf+St99z9v6DcaRfv2mCvk93wZ7+mGXo0by4+8t/gJYtXwfcpzf5cjMzBPWPKrxYKTlaahmutwlUhR40CGzE3tWqsRdy60OOkDUtgYLoR51edeZPRzMt/Afxh93SGddWL3wDmxSVvjqMKZSDFE/IgTZ93BFBY0lld07cLQn0SyDS1tGMjKU9Bo4hpB3dR8N9feZgsZdD7ytNfS0/T0J0m1O7S/NF+c8NGCpG8rfRx01/exnGvKAEqgsIiSf5FUtDPX1JZ/Ws0MYfMThAwKm9CDtsbNPmPkroDCNRh ubuntu@minikube\n"
    },
    "tags": {
        "app": "consul",
        "k8s_fwgroup": "consul",
        "k8s_namespace": "default",
        "k8s_nodename": "triton-vk",
        "k8s_uid": "88db751b-f75e-11e8-96a5-90b8d0b8add9",
        "pod-template-hash": "67f68648fc",
        "triton.cns.services": "consul"
    },
    "created": "2018-12-04T00:50:12.911Z",
    "updated": "2018-12-04T00:50:29.000Z",
    "networks": [
        "913849ab-17c4-4a03-9c2d-1147b8bf4d24",
        "50c48e19-a55b-4af8-9f06-c430f96c37ed"
    ],
    "primaryIp": "10.1.10.206",
    "firewall_enabled": true,
    "compute_node": "564d3083-8759-307e-27f4-f8c2b71eac68",
    "package": "dd-2gb-20gb",
    "dns_names": [
        "5213157a-fcc7-439f-bd3b-eb34dbeff3b3.inst.4b60d82b-d858-4c5a-ea3e-a0b9b411d65d.us-east-1.cns.mydomain",
        "nexus.5213157a-fcc7-439f-bd3b-eb34dbeff3b3.inst.4b60d82b-d858-4c5a-ea3e-a0b9b411d65d.us-east-1.cns.mydomain",
        "5213157a-fcc7-439f-bd3b-eb34dbeff3b3.inst.smith.us-east-1.cns.mydomain",
        "nexus.5213157a-fcc7-439f-bd3b-eb34dbeff3b3.inst.smith.us-east-1.cns.mydomain",
        "consul-67f68648fc-xb9wj.inst.4b60d82b-d858-4c5a-ea3e-a0b9b411d65d.us-east-1.cns.mydomain",
        "nexus.consul-67f68648fc-xb9wj.inst.4b60d82b-d858-4c5a-ea3e-a0b9b411d65d.us-east-1.cns.mydomain",
        "consul-67f68648fc-xb9wj.inst.smith.us-east-1.cns.mydomain",
        "nexus.consul-67f68648fc-xb9wj.inst.smith.us-east-1.cns.mydomain",
        "consul.svc.4b60d82b-d858-4c5a-ea3e-a0b9b411d65d.us-east-1.cns.mydomain",
        "nexus.consul.svc.4b60d82b-d858-4c5a-ea3e-a0b9b411d65d.us-east-1.cns.mydomain",
        "consul.svc.smith.us-east-1.cns.mydomain",
        "nexus.consul.svc.smith.us-east-1.cns.mydomain"
    ]
}

```

With the following Firewall Rules:
``` bash
/g/i/consul ❯❯❯ triton fwrules                                                                                                                                                                                                                                                                                                                                                                                                  k8s ✱ ◼
SHORTID   ENABLED  GLOBAL  RULE
1b63304e  true     -       FROM any TO vm 5213157a-fcc7-439f-bd3b-eb34dbeff3b3 ALLOW tcp PORT 22
1a8cae7a  true     -       FROM any TO vm 5213157a-fcc7-439f-bd3b-eb34dbeff3b3 ALLOW tcp PORT 8500
772f1671  true     -       FROM tag "k8s_consul" TO tag "k8s_consul" ALLOW tcp PORT all
de193e50  true     -       FROM tag "k8s_consul" TO tag "k8s_consul" ALLOW udp PORT all

```


