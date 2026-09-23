"""Verify the actual release archive and checksum bytes before upload."""
from pathlib import Path
import hashlib, re, sys, tarfile
root=Path(sys.argv[1])
version=sys.argv[2]
sums=(root/('SHA256SUMS-'+version+'.txt')).read_bytes()
assert b'\r' not in sums and sums.endswith(b'\n'), 'checksum manifest must use LF'
rows=sums.decode().splitlines()
assert len(rows)==2
for row in rows:
    digest,name=row.split()
    assert re.fullmatch('[a-f0-9]{64}',digest)
    assert hashlib.sha256((root/name).read_bytes()).hexdigest()==digest
archive=root/('fanout-integrated-'+version+'.tar.gz')
with tarfile.open(archive) as arc:
    names=arc.getnames()
    assert len(names)==len(set(names))
    for n in names:
        assert n.startswith('fanout-integrated/') and '..' not in Path(n).parts
        assert '__pycache__' not in n
        if n.endswith('.sh'):
            assert b'\r' not in arc.extractfile(n).read(), 'shell files must use LF'
    for name in ['install.sh','bootstrap.sh','core-install.sh','maintenance.sh','panel-config.py']:
        assert 'fanout-integrated/'+name in names
    for row in arc.extractfile('fanout-integrated/BIN_SHA256SUMS').read().decode().splitlines():
        digest,name=row.split()
        assert hashlib.sha256(arc.extractfile('fanout-integrated/'+name).read()).hexdigest()==digest
print('Release manifest, archive structure, shell line endings and both binaries verified')
