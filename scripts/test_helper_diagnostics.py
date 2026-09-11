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

    def test_application_records_redact_json_and_console(self):
        parser = module('helper-diagnostic-records').application_record
        fields = {'message':'vote HTTP timing','request_id':'a'*32,'route':'shares','method':'POST','body_bytes':42,'body_complete':True,'response_write_us':7,'payload':'private','error':'private','url':'private'}
        record = parser(json.dumps(fields))
        self.assertEqual(record['response_write_us'],7)
        self.assertTrue(record['body_complete'])
        self.assertNotIn('private',json.dumps(record))
        console = '12:00 INF vote HTTP phase request_id=' + 'b'*32 + ' route=chain_status phase=status_lookup duration_us=5 outcome=error error=private'
        record = parser(console)
        self.assertEqual(record['duration_us'],5)
        self.assertNotIn('private',json.dumps(record))
        self.assertIsNone(parser(console.replace('chain_status','private')))
        self.assertIsNone(parser('ordinary message payload=private'))

    def test_journal_byte_arrays_and_colored_console(self):
        parser = module('helper-diagnostic-records').application_record
        console = '\x1b[32mINF\x1b[0m vote HTTP timing \x1b[36mrequest_id=\x1b[0m' + 'e'*32 + ' \x1b[36mroute=\x1b[0mshares duration_us=12 token=private'
        record = parser(list(console.encode()))
        self.assertEqual(record['duration_us'], 12)
        self.assertNotIn('private', json.dumps(record))
        for invalid in ([256], [-1], [True], [255], ['private']):
            self.assertIsNone(parser(invalid))

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

    def test_correlated_boundaries_and_incomplete_capture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            diagnostic = {'request_id':'c'*32, 'route':'shares', 'protocol':'h2',
                          'response_headers_us':3000000, 'connection_predates_request':True,
                          'phase':'complete'}
            (root/'round.observability.json').write_text(json.dumps({'records':[{'http_diagnostics':diagnostic}]}))
            (root/'runtime-lag.json').write_text(json.dumps({'samples':[{'lag_us':12000}], 'dropped':2}))
            records = [
                {'caddy_access':{'request_id':'c'*32, 'duration':3.1, 'upstream_headers_ms':2000}},
                {'server_timing':{'request_id':'c'*32, 'duration_us':10000}},
                {'server_phase':{'request_id':'c'*32, 'phase':'broadcast', 'duration_us':8000}},
                {'server_journal_coverage':{'unstructured':1}},
                {'errors':['svoted:TimeoutError']},
            ]
            (root/'primary.jsonl').write_text('\n'.join(map(json.dumps, records))+'\n{truncated')
            result = module('analyze-helper-diagnostics').analyze([root], root)
            self.assertEqual(result['server_matched'], 1)
            self.assertEqual(result['headers_at_least_2s'], 1)
            self.assertEqual(result['timings']['headers_outside_upstream']['mean_ms'], 1000)
            self.assertEqual(result['timings']['rpc_broadcast']['mean_ms'], 8)
            self.assertEqual(result['timings']['runtime_lag']['mean_ms'], 12)
            self.assertEqual(result['runtime_dropped_samples'], 2)
            self.assertEqual(result['coverage']['malformed_capture_lines'], 1)
            self.assertEqual(result['coverage']['unstructured_server_records'], 1)
            self.assertEqual(result['groups']['shares/h2/existing']['requests'], 1)
            self.assertNotIn('c'*32, json.dumps(result))

    def test_nonfinite_timings_are_excluded(self):
        analyzer = module('analyze-helper-diagnostics')
        for value in ('NaN', 'Infinity', -1, None, 'unavailable'):
            self.assertIsNone(analyzer.milliseconds(value))
        self.assertEqual(analyzer.milliseconds('12.5'), 12.5)

    def test_remote_collector_compiles_without_connecting(self):
        collector = module('collect-helper-diagnostics')
        compile(collector.REMOTE, '<remote collector>', 'exec')

    def test_log_projection_excludes_sensitive_fields(self):
        project = module('helper-diagnostic-records').application_record
        identifier = 'd'*32
        for message in (json.dumps({'msg':'vote HTTP timing', 'request_id':identifier,
                                    'route':'shares', 'duration_us':123, 'token':'private',
                                    'body':'private', 'method':'POST'}),
                        'INF vote HTTP timing request_id='+identifier+' route=shares duration_us=123 token=private method=POST'):
            record = project(message)
            self.assertEqual(record['duration_us'], 123)
            self.assertNotIn('private', json.dumps(record))
            self.assertNotIn('token', record)
        self.assertIsNone(project('ordinary journal message token=private'))


if __name__ == '__main__':
    unittest.main()
