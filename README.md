# Node File Injector

A Kubernetes operator that synchronizes files from ConfigMaps or Secrets to node filesystems using DaemonSets.

## Description

Node File Injector is a Kubernetes operator that allows you to declaratively manage files on your cluster nodes. It watches for `NodeFileInjector` custom resources and creates DaemonSets that maintain files on selected nodes, keeping them synchronized with ConfigMap or Secret content.

### Key Features

- 🎯 **Declarative file management** - Define files on nodes using Kubernetes CRDs
- 🔄 **Automatic synchronization** - Files are automatically updated when ConfigMaps/Secrets change
- 🎨 **Node selection** - Use label selectors to target specific nodes
- 🔐 **Support for ConfigMaps and Secrets** - Source content from either resource type
- 🛡️ **File permissions** - Set owner, group, and mode (permissions)
- ⚡ **Real-time updates** - DaemonSet monitors and updates files continuously

### Use Cases

- Distributing configuration files to all nodes
- Managing SSL/TLS certificates on nodes
- Installing custom CA certificates
- Deploying monitoring agent configurations
- Setting up custom kernel modules configuration

## Getting Started

### Prerequisites
- go version v1.24.0+
- docker version 17.03+
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster

### Quick Start Example

1. **Install the CRDs:**

```sh
make install
```

2. **Run the operator locally (for testing):**

```sh
make run
```

3. **Create a ConfigMap with your file content:**

```sh
kubectl apply -f config/samples/configmap-sample.yaml
```

4. **Create a NodeFileInjector resource:**

```sh
kubectl apply -f config/samples/files_v1alpha1_nodefileinjector.yaml
```

5. **Check the status:**

```sh
kubectl get nodefileinjector
kubectl describe nodefileinjector nodefileinjector-sample
```

### NodeFileInjector Resource Spec

```yaml
apiVersion: files.barpilot.io/v1alpha1
kind: NodeFileInjector
metadata:
  name: my-config-file
spec:
  # Node selector - leave empty to target all nodes
  nodeSelector:
    kubernetes.io/os: linux
  
  # Source from ConfigMap
  source:
    configMap:
      name: my-config
      key: my-file.conf
  
  # OR source from Secret
  # source:
  #   secret:
  #     name: my-secret
  #     key: tls.crt
  
  # Destination path on the node
  path: /etc/myapp/config.conf
  
  # File permissions (octal)
  mode: "0644"
  
  # File owner (UID)
  owner: 0
  
  # File group (GID)
  group: 0
```

### Field Descriptions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `nodeSelector` | map[string]string | No | Kubernetes label selector for targeting specific nodes. Empty means all nodes. |
| `source.configMap` | ConfigMapKeySelector | Yes* | Reference to a ConfigMap key containing the file content. |
| `source.secret` | SecretKeySelector | Yes* | Reference to a Secret key containing the file content. |
| `path` | string | Yes | Absolute path on the node where the file will be written. Must start with `/`. |
| `mode` | string | No | File permissions in octal format (e.g., "0644"). Default: "0644" |
| `owner` | int64 | No | User ID that should own the file. Default: 0 |
| `group` | int64 | No | Group ID that should own the file. Default: 0 |

\* Exactly one of `configMap` or `secret` must be specified.

### Status Fields

The operator provides status information about the NodeFileInjector resource:

```yaml
status:
  conditions:
    - type: Ready
      status: "True"
      reason: Succeeded
      message: DaemonSet is ready
  daemonSetName: nfi-nodefileinjector-sample
  nodesMatched: 3
  observedGeneration: 1
```


### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/node-file-injector:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/node-file-injector:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/node-file-injector:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/node-file-injector/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

