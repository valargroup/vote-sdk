#!/usr/bin/env python3
"""Test the installer's MPTCP gate without changing host network settings."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SOURCE = (ROOT / "join.sh").read_text()
HELPER = re.search(r"(?ms)^disable_mptcp\(\) \{\n.*?^\}\n", SOURCE).group()


class MptcpTests(unittest.TestCase):
    def run_gate(self, *, initial="1", kernel="Linux", failure="", proc=True):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            net = root / "proc/sys/net"
            if proc:
                (net / "ipv4").mkdir(parents=True)
            if initial is not None:
                (net / "mptcp").mkdir(parents=True)
                (net / "mptcp/enabled").write_text(initial + "\n")
            helper = HELPER.replace("/proc/sys/net", str(net)).replace(
                "/etc/sysctl.d", str(root / "sysctl.d"))
            script = """set -euo pipefail
uname() { echo "$TEST_KERNEL"; }
sudo() { "$@"; }
install() {
  [ "$TEST_FAILURE" != directory ] || return 1
  command install "$@"
}
tee() {
  case "$1:$TEST_FAILURE" in
    *90-vote-disable-mptcp.conf:persist|*/mptcp/enabled:apply)
      command cat > /dev/null; return 1 ;;
    */mptcp/enabled:readback)
      command cat > /dev/null; return 0 ;;
  esac
  command tee "$@"
}
cat() {
  if [ "$TEST_FAILURE" = read ] && [[ "$1" = */mptcp/enabled ]]; then return 1; fi
  command cat "$@"
}
""" + helper + '\ndisable_mptcp\necho INSTALLATION_CONTINUED\n'
            env = dict(os.environ, TEST_KERNEL=kernel, TEST_FAILURE=failure)
            result = subprocess.run(["bash"], input=script, text=True,
                                    capture_output=True, env=env)
            config = root / "sysctl.d/90-vote-disable-mptcp.conf"
            setting = net / "mptcp/enabled"
            return (result, config.read_text() if config.exists() else None,
                    setting.read_text() if setting.exists() else None)

    def test_enabled_and_already_disabled(self):
        for initial in ("1", "0"):
            with self.subTest(initial=initial):
                result, config, setting = self.run_gate(initial=initial)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(config, "net.mptcp.enabled = 0\n")
                self.assertEqual(setting, "0\n")
                self.assertIn("INSTALLATION_CONTINUED", result.stdout)

    def test_already_disabled_readonly_setting(self):
        result, config, setting = self.run_gate(initial="0", failure="apply")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(config, "net.mptcp.enabled = 0\n")
        self.assertEqual(setting, "0\n")

    def test_kernel_without_mptcp(self):
        result, config, _ = self.run_gate(initial=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(config)
        self.assertIn("unavailable", result.stdout)

    def test_non_linux(self):
        result, config, setting = self.run_gate(kernel="Darwin")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(config)
        self.assertEqual(setting, "1\n")

    def test_missing_proc_is_not_treated_as_unsupported(self):
        result, _, _ = self.run_gate(initial=None, proc=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Cannot inspect", result.stderr)
        self.assertNotIn("INSTALLATION_CONTINUED", result.stdout)

    def test_failures_stop_installation(self):
        for failure in ("directory", "persist", "apply", "readback", "read"):
            with self.subTest(failure=failure):
                result, _, _ = self.run_gate(failure=failure)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("ERROR:", result.stderr)
                self.assertNotIn("INSTALLATION_CONTINUED", result.stdout)

    def test_gate_precedes_package_installation(self):
        self.assertLess(SOURCE.index("\ndisable_mptcp\n"),
                        SOURCE.index("\ninstall_missing_tools\n"))


if __name__ == "__main__":
    unittest.main()
