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

package utils

import (
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck
	. "github.com/onsi/gomega"    //nolint:staticcheck

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StopKubeletOnNode stops the kubelet service on a node via oc debug.
// This routes through the Kubernetes API server and requires no SSH access,
// making it compatible with Prow ephemeral clusters.
func StopKubeletOnNode(nodeName string) error {
	By(fmt.Sprintf("Stopping kubelet on node %s via oc debug", nodeName))
	cmd := exec.Command("oc", "debug", fmt.Sprintf("node/%s", nodeName),
		"--", "chroot", "/host", "systemctl", "stop", "kubelet")
	output, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to stop kubelet on node %s: %s: %w", nodeName, output, err)
	}
	GinkgoWriter.Printf("Kubelet stop command completed on node %s\n", nodeName)
	return nil
}

// StartKubeletOnNode starts the kubelet service on a node via oc debug.
func StartKubeletOnNode(nodeName string) error {
	By(fmt.Sprintf("Starting kubelet on node %s via oc debug", nodeName))
	cmd := exec.Command("oc", "debug", fmt.Sprintf("node/%s", nodeName),
		"--", "chroot", "/host", "systemctl", "start", "kubelet")
	output, err := Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to start kubelet on node %s: %s: %w", nodeName, output, err)
	}
	GinkgoWriter.Printf("Kubelet start command completed on node %s\n", nodeName)
	return nil
}

// WaitForNodeNotReady waits for a node to transition to NotReady status.
func WaitForNodeNotReady(tc *TestClients, nodeName string, timeout time.Duration) {
	By(fmt.Sprintf("Waiting for node %s to become NotReady", nodeName))
	Eventually(func() bool {
		node := &corev1.Node{}
		err := tc.Client.Get(tc.Context, client.ObjectKey{Name: nodeName}, node)
		if err != nil {
			GinkgoWriter.Printf("Failed to get node %s: %v\n", nodeName, err)
			return false
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s is NotReady (reason: %s)\n", nodeName, condition.Reason)
				return true
			}
		}
		return false
	}, timeout, time.Second*10).Should(BeTrue(), fmt.Sprintf("node %s should become NotReady", nodeName))
}

// WaitForNodeReady waits for a node to transition to Ready status.
func WaitForNodeReady(tc *TestClients, nodeName string, timeout time.Duration) {
	By(fmt.Sprintf("Waiting for node %s to become Ready", nodeName))
	Eventually(func() bool {
		node := &corev1.Node{}
		err := tc.Client.Get(tc.Context, client.ObjectKey{Name: nodeName}, node)
		if err != nil {
			GinkgoWriter.Printf("Failed to get node %s: %v\n", nodeName, err)
			return false
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				GinkgoWriter.Printf("Node %s is Ready\n", nodeName)
				return true
			}
		}
		return false
	}, timeout, time.Second*15).Should(BeTrue(), fmt.Sprintf("node %s should become Ready", nodeName))
}

// GetNodeBootID returns the current bootID for a node.
func GetNodeBootID(tc *TestClients, nodeName string) string {
	node := &corev1.Node{}
	Eventually(func() bool {
		err := tc.Client.Get(tc.Context, client.ObjectKey{Name: nodeName}, node)
		return err == nil && node.Status.NodeInfo.BootID != ""
	}, time.Minute*2, time.Second*10).Should(BeTrue(),
		fmt.Sprintf("should get bootID for node %s", nodeName))
	return node.Status.NodeInfo.BootID
}

// WaitForNodeReboot waits for a node's bootID to change, indicating a reboot occurred.
func WaitForNodeReboot(tc *TestClients, nodeName, originalBootID string, timeout time.Duration) {
	By(fmt.Sprintf("Waiting for node %s to reboot (bootID change)", nodeName))
	Eventually(func() bool {
		node := &corev1.Node{}
		err := tc.Client.Get(tc.Context, client.ObjectKey{Name: nodeName}, node)
		if err != nil {
			return false
		}
		currentBootID := node.Status.NodeInfo.BootID
		if currentBootID != "" && currentBootID != originalBootID {
			GinkgoWriter.Printf("Node %s rebooted: bootID changed from %s to %s\n",
				nodeName, originalBootID, currentBootID)
			return true
		}
		return false
	}, timeout, time.Second*30).Should(BeTrue(),
		fmt.Sprintf("node %s should reboot (bootID should change from %s)", nodeName, originalBootID))
}
