"""Durable inbox/protocol tests, not Docker/systemd or signed release acceptance."""
import json
import os
from pathlib import Path
import socket
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[2]/'infra/selfhost'))
import updater
import updater_bridge as bridge_module
from maintenance import Fence, durable_json, initialize
from updater_bridge import Bridge, ProtocolError, Server

ID = '11111111-1111-4111-8111-111111111111'
OTHER = '22222222-2222-4222-8222-222222222222'
DIGEST = 'a'*64


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.state = Path(self.temporary.name)
        self.patch = patch.object(updater, 'STATE', self.state)
        self.patch.start(); self.addCleanup(self.patch.stop)
        self.bridge = Bridge()
        self.catalog = patch.object(Bridge, 'candidate', return_value={'digest':DIGEST,'version':'test'})
        self.catalog.start(); self.addCleanup(self.catalog.stop)
        self.wake = patch.object(Bridge, 'wake')
        self.mock_wake = self.wake.start(); self.addCleanup(self.wake.stop)

    def test_persist_retry_restart_and_conflict(self):
        self.mock_wake.side_effect = OSError('private-diagnostic')
        with self.assertRaises(ProtocolError): self.bridge.start(ID,DIGEST)
        first = self.bridge.status(ID)
        self.mock_wake.side_effect = None
        self.assertEqual(first['phase'],'REQUESTED')
        self.assertEqual(Bridge().status(),first)
        self.assertEqual(Bridge().start(ID,DIGEST),first)
        with self.assertRaises(ProtocolError) as error:
            self.bridge.start(ID,'b'*64)
        self.assertEqual(error.exception.code,'conflict')
        with self.assertRaises(ProtocolError) as error:
            self.bridge.start(OTHER,DIGEST)
        self.assertEqual(error.exception.code,'busy')
        self.assertEqual(len(list((self.state/'requests').glob('*.json'))),1)

    def test_failed_wake_is_bounded_without_installation(self):
        self.mock_wake.side_effect = OSError('private-diagnostic')
        for _ in range(8):
            with self.assertRaises(ProtocolError): self.bridge.start(ID,DIGEST)
        self.assertEqual(self.bridge.start(ID,DIGEST)['phase'],'FAILED')
        self.assertEqual(self.mock_wake.call_count,8)
        self.assertIsNone(self.bridge.resume_requested())

    def test_wake_exhaustion_preserves_existing_job_and_gate(self):
        self.bridge.start(ID,DIGEST)
        directory=self.state/ID;directory.mkdir()
        durable_json(directory/'job.json',{'id':ID,'digest':DIGEST,'phase':'QUEUED',
                     'old':{'version':'old'},'target':{'version':'test'},'rollback_resumes':0})
        admission=self.state/'admission';initialize(admission)
        fence=Fence(admission)
        with fence.hold(ID): pass
        receipt=self.bridge.receipt(ID);receipt['wake_attempts']=8
        durable_json(self.bridge.request_path(ID),receipt)
        with self.assertRaises(ProtocolError): self.bridge.start(ID,DIGEST)
        self.assertEqual(self.bridge.status(ID)['phase'],'QUEUED')
        self.assertTrue(fence.active(ID))

    def test_interrupted_pointer_publication_and_exact_repair(self):
        def write(path,data):
            if path.name=='requested.json': raise OSError('interrupted publication')
            durable_json(path,data)
        with patch.object(bridge_module,'durable_json',side_effect=write):
            with self.assertRaises(OSError): self.bridge.start(ID,DIGEST)
        self.assertIsNone(self.bridge.requested())
        with self.assertRaises(ProtocolError) as error:
            self.bridge.start(OTHER,DIGEST)
        self.assertEqual(error.exception.code,'busy')
        self.bridge.start(ID,DIGEST)
        self.assertEqual(Bridge().requested(),ID)

    def test_queue_failure_is_durable_and_does_not_retry_installation(self):
        self.bridge.start(ID,DIGEST)
        with patch.object(updater,'queue',side_effect=ValueError('private-fixture-secret')) as queue:
            self.assertIsNone(self.bridge.resume_requested())
            self.assertIsNone(Bridge().resume_requested())
            self.assertEqual(queue.call_count,1)
        self.assertEqual(self.bridge.status(ID)['phase'],'FAILED')
        self.assertNotIn('private-fixture-secret',self.bridge.request_path(ID).read_text())

    def test_job_written_before_failure_is_not_erased(self):
        self.bridge.start(ID,DIGEST)
        def write_job(arguments):
            directory=self.state/ID;directory.mkdir()
            durable_json(directory/'job.json',{'id':ID,'digest':DIGEST,'phase':'QUEUED',
                         'old':{'version':'old'},'target':{'version':'test'},'rollback_resumes':0})
            raise OSError('pointer interruption')
        with patch.object(updater,'queue',side_effect=write_job):
            with self.assertRaises(OSError): self.bridge.resume_requested()
        self.assertEqual(Bridge().resume_requested(),ID)
        self.assertEqual(self.bridge.status(ID)['phase'],'QUEUED')

    def test_fixed_protocol_rejects_paths_and_wrong_types(self):
        for request in ({'protocol':1,'op':'shell'}, {'protocol':True,'op':'catalog'},
                        {'protocol':1,'op':'start','id':ID,'digest':DIGEST,'bundle':'/tmp/file'},
                        {'protocol':1,'op':'status','id':'../../escape'},
                        {'protocol':1,'op':'status','id':None}):
            with self.assertRaises(ProtocolError): self.bridge.handle(request)

    def test_completed_receipt_does_not_hide_newer_boot_job(self):
        self.bridge.start(ID,DIGEST)
        for job_id,phase in ((ID,'ROLLED_BACK'),(OTHER,'INSTALLING')):
            directory=self.state/job_id;directory.mkdir()
            durable_json(directory/'job.json',{'id':job_id,'digest':DIGEST,'phase':phase,
                         'old':{'version':'old'},'target':{'version':'test'},'rollback_resumes':0})
        durable_json(self.state/'current.json',{'job':OTHER})
        self.assertEqual(self.bridge.resume_requested() or updater.current(),OTHER)

    def test_recovery_required_receipt_is_not_skipped(self):
        self.bridge.start(ID,DIGEST)
        directory=self.state/ID;directory.mkdir()
        durable_json(directory/'job.json',{'id':ID,'digest':DIGEST,'phase':'RECOVERY_REQUIRED',
                     'old':{'version':'old'},'target':{'version':'test'},'rollback_resumes':0})
        self.assertEqual(self.bridge.resume_requested(),ID)
        other=self.state/OTHER;other.mkdir()
        durable_json(other/'job.json',{'id':OTHER,'digest':DIGEST,'phase':'INSTALLING',
                     'old':{'version':'old'},'target':{'version':'test'},'rollback_resumes':0})
        durable_json(self.state/'current.json',{'job':OTHER})
        with self.assertRaises(ValueError): self.bridge.resume_requested()

    @unittest.skipUnless(sys.platform=='linux','Linux SO_PEERCRED required')
    def test_actual_socket_peer_credentials_and_request_bounds(self):
        def exchange(data,uid):
            client,peer=socket.socketpair()
            server=Server(None,self.bridge,allowed_uid=uid)
            client.sendall(data)
            worker=threading.Thread(target=server.serve_connection,args=(peer,));worker.start()
            try:
                result=json.loads(client.recv(32768))
            finally:
                client.close();worker.join(10)
            self.assertFalse(worker.is_alive())
            return result
        self.assertEqual(exchange(b'{"protocol":1,"op":"status"}\n',os.getuid())['phase'],'IDLE')
        self.assertEqual(exchange(b'{"protocol":1,"op":"status"}\n',os.getuid()+1)['error'],'unauthorized')
        for data in (b'{"protocol":1,"op":"status","op":"catalog"}\n', b'x'*4097+b'\n'):
            self.assertEqual(exchange(data,os.getuid())['error'],'invalid_request')


if __name__=='__main__': unittest.main()
