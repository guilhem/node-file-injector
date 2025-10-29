//go:build e2e
// +build e2e

/*
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
*/

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/guilhem/node-file-injector/test/utils"
)

// namespace where the project is deployed in
const namespace = "node-file-injector-system"

// serviceAccountName created for the project
const serviceAccountName = "node-file-injector-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "node-file-injector-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "node-file-injector-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// Don't cleanup here - let tests continue using the deployed operator
	// Cleanup will happen in a separate AfterAll after all test blocks

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=node-file-injector-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, _ = utils.Run(cmd) // Ignore error if already exists

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})
})

var _ = Describe("NodeFileInjector File Injection", Ordered, func() {
	const testNamespace = "default"

	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	Context("ConfigMap-based file injection", func() {
		const (
			nfiName    = "test-cm-injection"
			configMap  = "test-nfi-cm"
			targetPath = "/tmp/test-nfi-cm/config.yaml"
		)

		BeforeAll(func() {
			By("creating test ConfigMap")
			cmd := exec.Command("kubectl", "create", "configmap", configMap,
				"--from-literal=config.yaml=initial-content-v1",
				"-n", testNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			By("cleaning up NodeFileInjector")
			cmd := exec.Command("kubectl", "delete", "nfi", nfiName,
				"-n", testNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			By("cleaning up ConfigMap")
			cmd = exec.Command("kubectl", "delete", "configmap", configMap,
				"-n", testNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should inject file from ConfigMap to node", func() {
			By("creating NodeFileInjector resource")
			nfiYaml := fmt.Sprintf(`
apiVersion: files.barpilot.io/v1alpha1
kind: NodeFileInjector
metadata:
  name: %s
  namespace: %s
spec:
  path: %s
  source:
    configMapKeyRef:
      name: %s
      key: config.yaml
  mode: "0644"
  owner: 0
  group: 0
`, nfiName, testNamespace, targetPath, configMap)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nfiYaml)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DaemonSet to be created")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					fmt.Sprintf("nfi-%s", nfiName),
					"-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(fmt.Sprintf("nfi-%s", nfiName)))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("waiting for DaemonSet pod to be running")
			var podName string
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods",
					"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", nfiName),
					"-n", testNamespace,
					"-o", "jsonpath={.items[0].metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty())
				podName = strings.TrimSpace(output)
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", podName,
					"-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(Equal("Running"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying file content on the node")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", podName,
					"-c", "file-injector",
					"-n", testNamespace,
					"--",
					"cat", fmt.Sprintf("/host%s", targetPath))
				output, err := utils.Run(cmd)
				if err != nil {
					// Debug logs on failure
					logCmd := exec.Command("kubectl", "logs", podName, "-c", "file-injector", "-n", testNamespace, "--tail=50")
					if logs, logErr := utils.Run(logCmd); logErr == nil {
						fmt.Printf("\n=== Pod logs (last 50 lines) ===\n%s\n", logs)
					}
				}
				g.Expect(err).NotTo(HaveOccurred())
				if strings.TrimSpace(output) != "initial-content-v1" {
					fmt.Printf("\n=== Unexpected file content ===\nExpected: 'initial-content-v1'\nGot: '%s'\n", strings.TrimSpace(output))
				}
				g.Expect(strings.TrimSpace(output)).To(Equal("initial-content-v1"))
			}, 1*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying file permissions (0644)")
			cmd = exec.Command("kubectl", "exec", podName,
				"-c", "file-injector",
				"-n", testNamespace,
				"--",
				"stat", "-c", "%a", fmt.Sprintf("/host%s", targetPath))
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("644"))

			By("verifying file ownership (0:0)")
			cmd = exec.Command("kubectl", "exec", podName,
				"-c", "file-injector",
				"-n", testNamespace,
				"--",
				"stat", "-c", "%u:%g", fmt.Sprintf("/host%s", targetPath))
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("0:0"))

			By("verifying NodeFileInjector status is Ready")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nfi", nfiName,
					"-n", testNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				if err != nil {
					// Debug: show full NFI status
					statusCmd := exec.Command("kubectl", "get", "nfi", nfiName, "-n", testNamespace, "-o", "yaml")
					if statusYaml, statusErr := utils.Run(statusCmd); statusErr == nil {
						fmt.Printf("\n=== NodeFileInjector status ===\n%s\n", statusYaml)
					}
				}
				g.Expect(err).NotTo(HaveOccurred())
				if strings.TrimSpace(output) != "True" {
					fmt.Printf("\n=== NodeFileInjector not Ready ===\nStatus: '%s'\n", strings.TrimSpace(output))
					// Show conditions
					condCmd := exec.Command("kubectl", "get", "nfi", nfiName, "-n", testNamespace, "-o", "jsonpath={.status.conditions}")
					if conditions, condErr := utils.Run(condCmd); condErr == nil {
						fmt.Printf("Conditions: %s\n", conditions)
					}
				}
				g.Expect(strings.TrimSpace(output)).To(Equal("True"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should update file when ConfigMap changes", func() {
			By("getting the current pod name")
			cmd := exec.Command("kubectl", "get", "pods",
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", nfiName),
				"-n", testNamespace,
				"-o", "jsonpath={.items[0].metadata.name}")
			podName, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			podName = strings.TrimSpace(podName)
			Expect(podName).NotTo(BeEmpty())

			By("updating ConfigMap content")
			cmd = exec.Command("kubectl", "patch", "configmap", configMap,
				"-n", testNamespace,
				"--type=json",
				"-p", `[{"op": "replace", "path": "/data/config.yaml", "value": "updated-content-v2"}]`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for file content to be updated (Kubernetes ConfigMap sync + script check)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", podName,
					"-c", "file-injector",
					"-n", testNamespace,
					"--",
					"cat", fmt.Sprintf("/host%s", targetPath))
				output, err := utils.Run(cmd)
				if err != nil {
					// Debug logs
					logCmd := exec.Command("kubectl", "logs", podName, "-c", "file-injector", "-n", testNamespace, "--tail=100")
					if logs, logErr := utils.Run(logCmd); logErr == nil {
						fmt.Printf("\n=== Pod logs (last 100 lines) ===\n%s\n", logs)
					}
					// Check ConfigMap content
					cmCmd := exec.Command("kubectl", "get", "configmap", "test-nfi-cm", "-n", testNamespace, "-o", "yaml")
					if cmYaml, cmErr := utils.Run(cmCmd); cmErr == nil {
						fmt.Printf("\n=== ConfigMap content ===\n%s\n", cmYaml)
					}
				}
				g.Expect(err).NotTo(HaveOccurred())
				currentContent := strings.TrimSpace(output)
				if currentContent != "updated-content-v2" {
					fmt.Printf("\n=== File not yet updated ===\nExpected: 'updated-content-v2'\nGot: '%s'\n", currentContent)
					// Check mounted volume content
					srcCmd := exec.Command("kubectl", "exec", podName, "-c", "file-injector", "-n", testNamespace, "--", "cat", "/source/content")
					if srcContent, srcErr := utils.Run(srcCmd); srcErr == nil {
						fmt.Printf("Source volume content: '%s'\n", strings.TrimSpace(srcContent))
					}
				}
				g.Expect(currentContent).To(Equal("updated-content-v2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying backup file was created")
			cmd = exec.Command("kubectl", "exec", podName,
				"-c", "file-injector",
				"-n", testNamespace,
				"--",
				"sh", "-c", fmt.Sprintf("ls %s.backup.* 2>/dev/null | wc -l", fmt.Sprintf("/host%s", targetPath)))
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			backupCount := strings.TrimSpace(output)
			Expect(backupCount).NotTo(Equal("0"), "At least one backup file should exist")
		})
	})

	Context("Secret-based file injection", func() {
		const (
			nfiName    = "test-secret-injection"
			secretName = "test-nfi-secret"
			targetPath = "/tmp/test-nfi-secret/token"
		)

		BeforeAll(func() {
			By("creating test Secret")
			cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
				"--from-literal=token=secret-token-value",
				"-n", testNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			By("cleaning up NodeFileInjector")
			cmd := exec.Command("kubectl", "delete", "nfi", nfiName,
				"-n", testNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)

			By("cleaning up Secret")
			cmd = exec.Command("kubectl", "delete", "secret", secretName,
				"-n", testNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("should inject file from Secret with restricted permissions", func() {
			By("creating NodeFileInjector resource with Secret source")
			nfiYaml := fmt.Sprintf(`
apiVersion: files.barpilot.io/v1alpha1
kind: NodeFileInjector
metadata:
  name: %s
  namespace: %s
spec:
  path: %s
  source:
    secretKeyRef:
      name: %s
      key: token
  mode: "0600"
  owner: 1000
  group: 1000
`, nfiName, testNamespace, targetPath, secretName)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nfiYaml)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for DaemonSet pod to be running")
			var podName string
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods",
					"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", nfiName),
					"-n", testNamespace,
					"-o", "jsonpath={.items[0].metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				podName = strings.TrimSpace(output)
				g.Expect(podName).NotTo(BeEmpty())
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", podName,
					"-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(Equal("Running"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying file was created with correct content")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", podName,
					"-c", "file-injector",
					"-n", testNamespace,
					"--",
					"test", "-f", fmt.Sprintf("/host%s", targetPath))
				_, err := utils.Run(cmd)
				if err != nil {
					// Debug logs
					logCmd := exec.Command("kubectl", "logs", podName, "-c", "file-injector", "-n", testNamespace, "--tail=50")
					if logs, logErr := utils.Run(logCmd); logErr == nil {
						fmt.Printf("\n=== Pod logs (last 50 lines) ===\n%s\n", logs)
					}
					// Check directory content
					lsCmd := exec.Command("kubectl", "exec", podName, "-c", "file-injector", "-n", testNamespace, "--", "ls", "-la", "/host/tmp/test-nfi-secret")
					if lsOutput, lsErr := utils.Run(lsCmd); lsErr == nil {
						fmt.Printf("\n=== Directory content ===\n%s\n", lsOutput)
					}
				}
				g.Expect(err).NotTo(HaveOccurred())
			}, 1*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying restrictive file permissions (0600)")
			cmd = exec.Command("kubectl", "exec", podName,
				"-c", "file-injector",
				"-n", testNamespace,
				"--",
				"stat", "-c", "%a", fmt.Sprintf("/host%s", targetPath))
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("600"))

			By("verifying file ownership (1000:1000)")
			cmd = exec.Command("kubectl", "exec", podName,
				"-c", "file-injector",
				"-n", testNamespace,
				"--",
				"stat", "-c", "%u:%g", fmt.Sprintf("/host%s", targetPath))
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("1000:1000"))
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
