package microtest

import (
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("E2E test", func() {
	testNamespacePrefix := "e2e-test"

	It("should be mounted in specified path", func() {
		ns := testNamespacePrefix + randomString(10)
		createNamespace(ns)

		By("initialize LogicalVolume CRD")
		createNamespace("topolvm-system")
		stdout, stderr, err := kubectl("apply", "-f", "../topolvm-node/config/crd/bases/topolvm.cybozu.com_logicalvolumes.yaml")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		By("initialize topolvm services")
		stdout, stderr, err = kubectl("apply", "-f", "./csi.yml")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		By("deploying Pod with PVC")
		podYAML := `apiVersion: v1
kind: Pod
metadata:
  name: ubuntu
  labels:
    app.kubernetes.io/name: ubuntu
spec:
  containers:
    - name: ubuntu
      image: quay.io/cybozu/ubuntu:18.04
      command: ["sleep", "infinity"]
      volumeMounts:
        - mountPath: /test1
          name: my-volume
  volumes:
    - name: my-volume
      persistentVolumeClaim:
        claimName: topo-pvc
`
		podAndClaimYAML := `kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: topo-pvc
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: topolvm-provisioner
---
` + podYAML
		stdout, stderr, err = kubectlWithInput([]byte(podAndClaimYAML), "apply", "-n", ns, "-f", "-")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		By("confirming that the specified device exists in the Pod")
		Eventually(func() error {
			stdout, stderr, err = kubectl("get", "pvc", "topo-pvc", "-n", ns)
			if err != nil {
				return fmt.Errorf("failed to create PVC. stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			stdout, stderr, err = kubectl("get", "pods", "ubuntu", "-n", ns)
			if err != nil {
				return fmt.Errorf("failed to create Pod. stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			stdout, stderr, err = kubectl("exec", "-n", ns, "ubuntu", "--", "mountpoint", "-d", "/test1")
			if err != nil {
				return fmt.Errorf("failed to check mount point. stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}
			return nil
		}).Should(Succeed())

		By("writing file under /test1")
		writePath := "/test1/bootstrap.log"
		stdout, stderr, err = kubectl("exec", "-n", ns, "ubuntu", "--", "cp", "/var/log/bootstrap.log", writePath)
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
		stdout, stderr, err = kubectl("exec", "-n", ns, "ubuntu", "--", "sync")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
		stdout, stderr, err = kubectl("exec", "-n", ns, "ubuntu", "--", "cat", writePath)
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
		Expect(strings.TrimSpace(string(stdout))).ShouldNot(BeEmpty())

		By("deleting the Pod, then recreating it")
		stdout, stderr, err = kubectl("delete", "--now=true", "-n", ns, "pod/ubuntu")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
		stdout, stderr, err = kubectlWithInput([]byte(podYAML), "apply", "-n", ns, "-f", "-")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		By("confirming that the file exists")
		Eventually(func() error {
			stdout, stderr, err = kubectl("get", "pvc", "topo-pvc", "-n", ns)
			if err != nil {
				return fmt.Errorf("failed to create PVC. stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			stdout, stderr, err = kubectl("get", "pods", "ubuntu", "-n", ns)
			if err != nil {
				return fmt.Errorf("failed to create Pod. stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			stdout, stderr, err = kubectl("exec", "-n", ns, "ubuntu", "--", "cat", writePath)
			if err != nil {
				return fmt.Errorf("failed to cat. stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}
			if len(strings.TrimSpace(string(stdout))) == 0 {
				return fmt.Errorf(writePath + " is empty")
			}
			return nil
		}).Should(Succeed())

		By("confirming that the lv correspond to LogicalVolume resource is registered in LVM")
		stdout, stderr, err = kubectl("get", "pvc", "-n", ns, "topo-pvc", "-o=template", "--template={{.spec.volumeName}}")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
		volName := strings.TrimSpace(string(stdout))
		Eventually(func() error {
			stdout, stderr, err = kubectl("get", "logicalvolume", "-n", "topolvm-system", volName, "-o=template", "--template={{.metadata.uid}}")
			if err != nil {
				return fmt.Errorf("err=%v, stdout=%s, stderr=%s", err, stdout, stderr)
			}
			lvName := strings.TrimSpace(string(stdout))
			stdout, err = exec.Command("sudo", "lvdisplay", "--select", "lv_name="+lvName).Output()
			if err != nil {
				return fmt.Errorf("err=%v, stdout=%s", err, stdout)
			}
			if strings.TrimSpace(string(stdout)) == "" {
				return fmt.Errorf("lv_name ( %s ) not found", lvName)
			}
			return nil
		}).Should(Succeed())

		By("deleting the Pod and PVC")
		stdout, stderr, err = kubectlWithInput([]byte(podAndClaimYAML), "delete", "-n", ns, "-f", "-")
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		By("confirming that the PV is deleted")
		Eventually(func() error {
			stdout, stderr, err = kubectl("get", "pv", volName, "--ignore-not-found")
			if err != nil {
				return fmt.Errorf("failed to get pv/%s. stdout: %s, stderr: %s, err: %v", volName, stdout, stderr, err)
			}
			if len(strings.TrimSpace(string(stdout))) != 0 {
				return fmt.Errorf("target PV exists %s", volName)
			}
			return nil
		}).Should(Succeed())

		By("confirming that the lv correspond to LogicalVolume is deleted")
		Eventually(func() error {
			stdout, err = exec.Command("sudo", "lvdisplay", "--select", "lv_name="+volName).Output()
			if err != nil {
				return fmt.Errorf("failed to lvdisplay. stdout: %s, err: %v", stdout, err)
			}
			if len(strings.TrimSpace(string(stdout))) != 0 {
				return fmt.Errorf("target LV exists %s", volName)
			}
			return nil
		}).Should(Succeed())
	})
})

func waitCreatingDefaultSA(ns string) error {
	stdout, stderr, err := kubectl("get", "sa", "-n", ns, "default")
	if err != nil {
		return fmt.Errorf("default sa is not found. stdout=%s, stderr=%s, err=%v", stdout, stderr, err)
	}
	return nil
}
