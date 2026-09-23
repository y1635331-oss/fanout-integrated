import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('panel_config', Path(__file__).with_name('panel-config.py'))
panel = importlib.util.module_from_spec(spec)
spec.loader.exec_module(panel)

class PanelConfigTest(unittest.TestCase):
    def test_health_uses_normalized_path(self):
        from unittest.mock import MagicMock
        with tempfile.TemporaryDirectory() as d:
            data = Path(d)
            (data/'settings.json').write_text('{"port":8899,"listen_addr":"127.0.0.1"}')
            opener = MagicMock()
            opener.open.return_value.__enter__.return_value.status = 200
            for raw, expected in [('abc123', '/abc123'), ('/abc123/', '/abc123'), ('', '')]:
                (data/'basepath').write_text(raw)
                with patch.object(panel,'DATA',data), patch.object(panel.urllib.request,'build_opener',return_value=opener):
                    self.assertTrue(panel.health())
                    opener.open.assert_called_with('https://127.0.0.1:8899'+expected+'/', timeout=2)

    def test_domain_rejects_urls_and_injection(self):
        for value in ('https://panel.example.com', 'a.example.com:443', 'x.example.com;echo', '127.0.0.1', '*.example.com', '-a.example.com'):
            with self.assertRaises(ValueError):
                panel.valid_domain(value)
        self.assertEqual(panel.valid_domain('Panel.Example.com'), 'panel.example.com')

    def test_certificate_validation(self):
        with tempfile.TemporaryDirectory() as d:
            cert, key = str(Path(d)/'fullchain.pem'), str(Path(d)/'key.pem')
            subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',key,'-out',cert,'-days','1','-subj','/CN=panel.example.com','-addext','subjectAltName=DNS:panel.example.com'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            cfg = dict(domain='panel.example.com',cert=cert,key=key)
            self.assertIn('notAfter=', panel.check(cfg))
            with self.assertRaises(ValueError):
                panel.check(dict(cfg,domain='wrong.example.com'))

    def test_configuration_failure_restores_existing_settings(self):
        with tempfile.TemporaryDirectory() as d:
            data = Path(d)
            original = b'{"port":8899,"listen_addr":"127.0.0.1"}'
            (data/'settings.json').write_bytes(original)
            with patch.object(panel,'DATA',data), patch.object(panel,'check',return_value='valid'), patch.object(panel.subprocess,'run',side_effect=[RuntimeError('restart failed'),None]):
                with self.assertRaises(RuntimeError):
                    panel.configure(['panel.example.com','/tmp/cert.pem','/tmp/key.pem'])
            self.assertEqual((data/'settings.json').read_bytes(),original)
            self.assertFalse((data/'tls.json').exists())

if __name__ == '__main__':
    unittest.main()
