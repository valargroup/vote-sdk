"""Hermetic tests for diagnostic config preservation and correlation coverage."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


def module(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


class DiagnosticsTests(unittest.TestCase):
    def test_overlay_preserves_listeners_and_backend_routes(self):
        current = {'apps':{'http':{'servers':{'public':{'listen':[':443'],'routes':[{'handle':[{'handler':'reverse_proxy','upstreams':[{'dial':'127.0.0.1:1317'}]}]}]}}}}}
        diagnostic_routes = [{'handle':[{'handler':'vars','log_skip':True}]}]
        adapted = {'logging':{'logs':{'helper_diagnostics':{'include':['http.log.access.helper_diagnostics']}}},'apps':{'http':{'servers':{'dummy':{'logs':{},'routes':[{'terminal':True,'match':[{'host':['127.0.0.1']}],'handle':[{'handler':'subroute','routes':diagnostic_routes}]}]}}}}}
        candidate = module('helper-diagnostics-caddy').overlay(current,adapted)
        server = candidate['apps']['http']['servers']['public']
        self.assertEqual(server['listen'],[':443'])
        self.assertEqual(server['routes'][1:],current['apps']['http']['servers']['public']['routes'])
        self.assertNotIn('match',server['routes'][0])
        self.assertNotIn('terminal',server['routes'][0])
        self.assertEqual(server['logs'],{'default_logger_name':'helper_diagnostics'})
        with self.assertRaises(ValueError):
            module('helper-diagnostics-caddy').overlay(candidate,adapted)

    def test_analysis_exposes_missing_correlations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            report = {'records':[{'http_diagnostics':{'request_id':'a'*32,'protocol':'h2','response_headers_us':100000,'server_handler_us':10000,'unattributed_wait_us':90000}},{'http_diagnostics':{'request_id':'b'*32}}],'records_dropped':1}
            (root/'round.observability.json').write_text(json.dumps(report))
            (root/'primary.jsonl').write_text(json.dumps({'caddy_access':{'request_id':'a'*32,'duration':.015,'upstream_headers_ms':11}})+'\n')
            result = module('analyze-helper-diagnostics').analyze([root],root)
            self.assertEqual(result['proxy_missing'],1)
            self.assertEqual(result['dropped_observations'],1)
            self.assertEqual(result['timings']['handler']['mean_ms'],10)
            self.assertEqual(result['timings']['caddy']['mean_ms'],15)
            self.assertNotIn('a'*32,json.dumps(result))


if __name__ == '__main__':
    unittest.main()
