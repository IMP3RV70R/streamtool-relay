import os
from pathlib import Path
import subprocess
import sys
import tempfile

sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'infra/selfhost'))
from maintenance import Fence,initialize
with tempfile.TemporaryDirectory() as temporary:
    directory=Path(temporary)/'admission'
    initialize(directory)
    fence=Fence(directory)
    def probe(active):
        environment={**os.environ,'STREAMTOOL_GATE_INTEROP_DIRECTORY':str(directory),'STREAMTOOL_GATE_EXPECT_ACTIVE':str(active)}
        subprocess.run(['/gate-probe','-test.run','^TestHostFenceInterop$'],env=environment,check=True,stdout=subprocess.DEVNULL)
    probe(0)
    with fence.hold('linux-interoperability'):
        probe(1)
    probe(1)
    fence.release('linux-interoperability')
    probe(0)
print('PASS: Linux Python exclusive host lock and durable marker fence Go admissions across processes')
