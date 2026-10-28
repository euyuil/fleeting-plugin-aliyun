"""Network probes require explicit official endpoints before network access."""
import os
from pathlib import Path
import runpy
import unittest
from unittest import mock


class NetworkProbeTests(unittest.TestCase):
    def test_missing_or_arbitrary_endpoints_never_reach_network(self):
        probe = runpy.run_path(str(Path(__file__).with_name('network-probe.py')))["probe"]
        env = {"NETWORK_PROBE_HOST": "8.8.8.8", "NETWORK_PROBE_HOST_KEY_SHA256": "SHA256:synthetic"}
        for endpoints in ('', '[]', '["ecs.example.com"]', '["ecs.region.aliyuncs.com/path"]', '[true]', 'null'):
            with self.subTest(endpoints=endpoints), mock.patch.dict(os.environ, {**env, "NETWORK_PROBE_ENDPOINTS": endpoints}):
                with mock.patch('socket.getaddrinfo') as dns, mock.patch('subprocess.run') as scan:
                    with self.assertRaises(ValueError):
                        probe()
                    dns.assert_not_called()
                    scan.assert_not_called()


if __name__ == '__main__':
    unittest.main()
