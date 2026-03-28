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

// POC tests migrated from ocp-edge-auto MR !3386 (Maxim Alter)
// These tests validate the Prow migration architecture.
// See: /home/ugreener/Repos/ugreener/docs/sbr-operator/sbr-prow-migration-poc-plan.md

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	medik8sv1alpha1 "github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/test/utils"
)

var _ = Describe("SBR Migrated Tests (POC)", Ordered, Label("poc", "migrated"), func() {

	Context("Operator Installation Validation", func() {
		// Non-disruptive tests that verify the operator was installed correctly via OLM.
		// These are the first tests to run — if OLM installation failed, everything else is moot.

		It("should verify SBR controller manager is running", Label("smoke"), func() {
			// Port of: test_SBR_controller_manager_available (MR !3386)
			// Validates that the OLM-installed operator has a running controller-manager pod.

			By("Listing controller-manager pods in operator namespace")
			pods := &corev1.PodList{}
			err := k8sClient.List(ctx, pods,
				client.InNamespace(testNamespace.OperatorNamespace().Name),
				client.MatchingLabels{"control-plane": "controller-manager"})
			Expect(err).NotTo(HaveOccurred(), "failed to list controller-manager pods")

			// Filter out pods being deleted
			var activePods []corev1.Pod
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp == nil {
					activePods = append(activePods, pod)
				}
			}

			By("Verifying exactly one controller-manager pod is active")
			Expect(activePods).To(HaveLen(1),
				"expected exactly 1 active controller-manager pod, found %d", len(activePods))

			controllerPod := activePods[0]

			By("Verifying controller-manager pod is Running")
			Expect(controllerPod.Status.Phase).To(Equal(corev1.PodRunning),
				"controller-manager pod should be Running, got %s", controllerPod.Status.Phase)

			By("Verifying all containers in the pod are ready")
			for _, containerStatus := range controllerPod.Status.ContainerStatuses {
				Expect(containerStatus.Ready).To(BeTrue(),
					"container %s should be ready", containerStatus.Name)
				Expect(containerStatus.RestartCount).To(BeNumerically("<", 5),
					"container %s has too many restarts (%d)", containerStatus.Name, containerStatus.RestartCount)
			}

			GinkgoWriter.Printf("Controller-manager pod %s is running on node %s\n",
				controllerPod.Name, controllerPod.Spec.NodeName)
		})

		It("should verify all SBR CRDs are installed", Label("smoke"), func() {
			// Port of: test_SBR_CRDs (MR !3386)
			// Validates that all expected CRDs were installed by OLM.

			By("Querying API resources for SBR group")
			apiResourceList, err := testClients.Clientset.Discovery().ServerResourcesForGroupVersion(
				"storage-based-remediation.medik8s.io/v1alpha1")
			Expect(err).NotTo(HaveOccurred(),
				"failed to get API resources for storage-based-remediation.medik8s.io/v1alpha1")

			expectedCRDs := map[string]bool{
				"SBDConfig":                       false,
				"StorageBasedRemediation":         false,
				"StorageBasedRemediationTemplate": false,
			}

			By("Verifying each expected CRD is present")
			for _, resource := range apiResourceList.APIResources {
				if _, expected := expectedCRDs[resource.Kind]; expected {
					expectedCRDs[resource.Kind] = true
					GinkgoWriter.Printf("Found CRD: %s (name: %s)\n", resource.Kind, resource.Name)
				}
			}

			for crd, found := range expectedCRDs {
				Expect(found).To(BeTrue(), "expected CRD %s to be installed", crd)
			}

			GinkgoWriter.Printf("All %d expected CRDs are installed\n", len(expectedCRDs))
		})
	})

	Context("Agent Deployment", func() {

		It("should deploy SBD agent DaemonSet on all worker nodes", func() {
			// Port of: test_daemonset_runs_on_all_eligible_nodes (MR !3386)
			// Creates an SBDConfig and verifies the agent DaemonSet deploys on all workers.

			By("Discovering worker nodes")
			nodes := &corev1.NodeList{}
			Expect(k8sClient.List(ctx, nodes)).To(Succeed())

			var workerNodes []corev1.Node
			for _, node := range nodes.Items {
				isControlPlane := false
				for label := range node.Labels {
					if strings.Contains(label, "control-plane") || strings.Contains(label, "master") {
						isControlPlane = true
						break
					}
				}
				if !isControlPlane {
					workerNodes = append(workerNodes, node)
				}
			}
			Expect(len(workerNodes)).To(BeNumerically(">=", 3),
				"POC requires at least 3 worker nodes (R6), found %d", len(workerNodes))
			GinkgoWriter.Printf("Found %d worker nodes\n", len(workerNodes))

			By("Finding an RWX-compatible storage class")
			storageClasses := &storagev1.StorageClassList{}
			Expect(k8sClient.List(ctx, storageClasses)).To(Succeed())

			var rwxStorageClassName string
			for _, sc := range storageClasses.Items {
				if isRWXCompatibleProvisioner(sc.Provisioner) {
					rwxStorageClassName = sc.Name
					GinkgoWriter.Printf("Using RWX storage class: %s (provisioner: %s)\n",
						sc.Name, sc.Provisioner)
					break
				}
			}
			Expect(rwxStorageClassName).NotTo(BeEmpty(),
				"no RWX-compatible storage class found — ODF must be installed (R7)")

			By("Creating SBDConfig")
			configName := fmt.Sprintf("poc-daemonset-%d", time.Now().Unix())
			sbdConfig, err := testNamespace.CreateSBDConfig(configName,
				func(config *medik8sv1alpha1.SBDConfig) {
					config.Spec.SharedStorageClass = rwxStorageClassName
					config.Spec.SbdWatchdogPath = "/dev/watchdog"
					config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second}
				})
			Expect(err).NotTo(HaveOccurred(), "failed to create SBDConfig")

			By("Waiting for SBD agent DaemonSet")
			validator := testNamespace.NewSBDAgentValidator()
			opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
			opts.MinReadyPods = len(workerNodes)
			err = validator.ValidateAgentDeployment(opts)
			Expect(err).NotTo(HaveOccurred(), "SBD agent deployment validation failed")

			By("Verifying DaemonSet covers all worker nodes")
			daemonSets := &appsv1.DaemonSetList{}
			Expect(k8sClient.List(ctx, daemonSets,
				client.InNamespace(testNamespace.Name),
				client.MatchingLabels{"sbdconfig": sbdConfig.Name})).To(Succeed())
			Expect(daemonSets.Items).To(HaveLen(1), "expected exactly 1 DaemonSet")

			ds := daemonSets.Items[0]
			Expect(ds.Status.DesiredNumberScheduled).To(Equal(int32(len(workerNodes))),
				"DaemonSet should be scheduled on all %d worker nodes", len(workerNodes))
			Expect(ds.Status.NumberReady).To(Equal(int32(len(workerNodes))),
				"all %d DaemonSet pods should be ready", len(workerNodes))

			GinkgoWriter.Printf("DaemonSet %s: desired=%d, ready=%d — all worker nodes covered\n",
				ds.Name, ds.Status.DesiredNumberScheduled, ds.Status.NumberReady)

			By("Cleaning up SBDConfig")
			Expect(testNamespace.CleanupSBDConfig(sbdConfig)).To(Succeed())
		})
	})

	Context("Node Remediation via NHC", Serial, func() {
		// Disruptive tests — must run serially (R4).
		// These tests stop kubelet via oc debug (R2), causing node failure and remediation.

		It("should remediate a node when kubelet is stopped via NHC", func() {
			// Port of: test_SBR_remediation_from_nhc (MR !3386)
			// Full remediation flow: stop kubelet -> NHC detects -> SBR remediates -> node reboots.

			By("Finding an RWX-compatible storage class")
			storageClasses := &storagev1.StorageClassList{}
			Expect(k8sClient.List(ctx, storageClasses)).To(Succeed())

			var rwxStorageClassName string
			for _, sc := range storageClasses.Items {
				if isRWXCompatibleProvisioner(sc.Provisioner) {
					rwxStorageClassName = sc.Name
					break
				}
			}
			Expect(rwxStorageClassName).NotTo(BeEmpty(),
				"no RWX-compatible storage class found")

			By("Creating SBDConfig for remediation test")
			configName := fmt.Sprintf("poc-nhc-%d", time.Now().Unix())
			sbdConfig, err := testNamespace.CreateSBDConfig(configName,
				func(config *medik8sv1alpha1.SBDConfig) {
					config.Spec.SharedStorageClass = rwxStorageClassName
					config.Spec.SbdWatchdogPath = "/dev/watchdog"
					config.Spec.WatchdogTimeout = &metav1.Duration{Duration: 90 * time.Second}
					config.Spec.RebootMethod = "panic" // Allow actual reboot for this test
				})
			Expect(err).NotTo(HaveOccurred(), "failed to create SBDConfig")

			By("Waiting for SBD agents to be ready")
			validator := testNamespace.NewSBDAgentValidator()
			opts := utils.DefaultValidateAgentDeploymentOptions(sbdConfig.Name)
			err = validator.ValidateAgentDeployment(opts)
			Expect(err).NotTo(HaveOccurred(), "SBD agent deployment failed")

			By("Selecting target worker node")
			targetNode := selectWorkerNode(clusterInfo)
			GinkgoWriter.Printf("Target node for remediation: %s\n", targetNode.Metadata.Name)

			By("Recording original bootID")
			originalBootID := utils.GetNodeBootID(testClients, targetNode.Metadata.Name)
			GinkgoWriter.Printf("Original bootID for %s: %s\n", targetNode.Metadata.Name, originalBootID)

			By("Deploying pinned workload on target node")
			workloadPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("poc-workload-%d", time.Now().Unix()),
					Namespace: testNamespace.Name,
					Labels:    map[string]string{"app": "poc-workload"},
				},
				Spec: corev1.PodSpec{
					NodeName:      targetNode.Metadata.Name,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "workload",
						Image:   "registry.access.redhat.com/ubi9/ubi-minimal:latest",
						Command: []string{"/bin/bash", "-c", "sleep 3600"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, workloadPod)).To(Succeed(), "failed to create workload pod")

			By("Stopping kubelet on target node via oc debug (R2)")
			// DeferCleanup ensures kubelet is restarted even if the test fails mid-way
			DeferCleanup(func() {
				GinkgoWriter.Printf("DeferCleanup: ensuring kubelet is running on %s\n", targetNode.Metadata.Name)
				_ = utils.StartKubeletOnNode(targetNode.Metadata.Name)
			})
			err = utils.StopKubeletOnNode(targetNode.Metadata.Name)
			Expect(err).NotTo(HaveOccurred(), "failed to stop kubelet")

			By("Waiting for node to become NotReady")
			utils.WaitForNodeNotReady(testClients, targetNode.Metadata.Name, time.Minute*5)

			By("Waiting for StorageBasedRemediation CR to be created by NHC")
			Eventually(func() bool {
				remediations := &medik8sv1alpha1.StorageBasedRemediationList{}
				err := k8sClient.List(ctx, remediations, client.InNamespace(testNamespace.Name))
				if err != nil {
					return false
				}
				for _, r := range remediations.Items {
					if r.Name == targetNode.Metadata.Name || strings.Contains(r.Name, targetNode.Metadata.Name) {
						GinkgoWriter.Printf("Found StorageBasedRemediation CR: %s\n", r.Name)
						return true
					}
				}
				return false
			}, time.Minute*10, time.Second*15).Should(BeTrue(),
				"NHC should create a StorageBasedRemediation CR for node %s", targetNode.Metadata.Name)

			By("Waiting for node reboot (bootID change)")
			utils.WaitForNodeReboot(testClients, targetNode.Metadata.Name, originalBootID, time.Minute*15)

			By("Waiting for node to become Ready again")
			utils.WaitForNodeReady(testClients, targetNode.Metadata.Name, time.Minute*10)

			By("Cleaning up remediated workloads")
			cleanupRemediatedWorkloads(testNamespace, targetNode.Metadata.Name)

			By("Cleaning up SBDConfig")
			Expect(testNamespace.CleanupSBDConfig(sbdConfig)).To(Succeed())

			GinkgoWriter.Printf("NHC remediation test completed successfully for node %s\n",
				targetNode.Metadata.Name)
		})
	})
})
